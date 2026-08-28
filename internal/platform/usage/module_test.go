package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newTestModule(t *testing.T) (http.Handler, *Module) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard))
	m.UseWorkloads(everyWorkload{})
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mux := http.NewServeMux()
	m.Routes(mux)
	return mux, m
}

func request(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func read(t *testing.T, h http.Handler) listResponse {
	t.Helper()

	var nodes listResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/usage/nodes", "").Body.Bytes(), &nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}

	var instances listResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/usage", "").Body.Bytes(), &instances); err != nil {
		t.Fatalf("decode instances: %v", err)
	}

	return listResponse{Nodes: nodes.Nodes, Instances: instances.Instances}
}

type everyWorkload struct{}

func (everyWorkload) IDsIn(context.Context, string) ([]string, error) {
	return []string{"i-1", "i-2", "i-3", "i-4"}, nil
}

func TestANodeReportReplacesTheLastOne(t *testing.T) {
	h, _ := newTestModule(t)

	first := `{"cpu_percent":12.5,"memory_used_mib":2000,"memory_mib":6000,
		"instances":[{"instance_id":"i-1","cpu_percent":3.5,"memory_used_mib":120}]}`
	if rec := request(t, h, http.MethodPut, "/v1/nodes/n-1/usage", first); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	second := `{"cpu_percent":40,"memory_used_mib":3000,"memory_mib":6000,
		"instances":[{"instance_id":"i-2","cpu_percent":1,"memory_used_mib":90}]}`
	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage", second)

	body := read(t, h)
	if len(body.Nodes) != 1 || body.Nodes[0].CPUPercent != 40 {
		t.Fatalf("nodes = %+v, want one row holding the latest sample", body.Nodes)
	}
	if len(body.Instances) != 1 || body.Instances[0].InstanceID != "i-2" {
		t.Fatalf("instances = %+v, want the previous pass forgotten: an instance that stopped "+
			"reporting must not look alive forever", body.Instances)
	}
}

func TestTwoNodesDoNotOverwriteEachOther(t *testing.T) {
	h, _ := newTestModule(t)

	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":10,"memory_used_mib":1000,"memory_mib":6000,
		  "instances":[{"instance_id":"i-1","cpu_percent":1,"memory_used_mib":10}]}`)
	request(t, h, http.MethodPut, "/v1/nodes/n-2/usage",
		`{"cpu_percent":20,"memory_used_mib":2000,"memory_mib":6000,
		  "instances":[{"instance_id":"i-2","cpu_percent":2,"memory_used_mib":20}]}`)

	body := read(t, h)
	if len(body.Nodes) != 2 || len(body.Instances) != 2 {
		t.Fatalf("usage = %+v / %+v, want both nodes kept", body.Nodes, body.Instances)
	}
}

func TestForgettingANode(t *testing.T) {
	h, m := newTestModule(t)

	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":10,"memory_used_mib":1000,"memory_mib":6000,
		  "instances":[{"instance_id":"i-1","cpu_percent":1,"memory_used_mib":10}]}`)

	if err := m.Forget(context.Background(), "n-1"); err != nil {
		t.Fatalf("forget: %v", err)
	}

	body := read(t, h)
	if len(body.Nodes) != 0 || len(body.Instances) != 0 {
		t.Fatalf("usage = %+v / %+v, want a removed node to leave no numbers behind",
			body.Nodes, body.Instances)
	}
}

func TestBadReportsAreRejected(t *testing.T) {
	h, _ := newTestModule(t)

	cases := map[string]struct {
		path string
		body string
		want int
	}{
		"unknown field": {"/v1/nodes/n-1/usage",
			`{"cpu_percent":10,"cpu_count":4}`, http.StatusBadRequest},
		"instance with no id": {"/v1/nodes/n-1/usage",
			`{"cpu_percent":10,"instances":[{"cpu_percent":1}]}`, http.StatusNoContent},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := request(t, h, http.MethodPut, want.path, want.body); rec.Code != want.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, want.want, rec.Body.String())
			}
		})
	}

	body := read(t, h)
	if len(body.Instances) != 0 {
		t.Fatalf("instances = %+v, want a sample with no id dropped rather than stored", body.Instances)
	}
}
