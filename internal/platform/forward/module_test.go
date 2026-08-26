package forward

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type fakeAddresses struct {
	endpoints map[string]Endpoint
}

func (f fakeAddresses) Endpoint(_ context.Context, instanceID string) (Endpoint, error) {
	endpoint, known := f.endpoints[instanceID]
	if !known {
		return Endpoint{}, fault.NotFound("nic_not_found", "the instance has no address")
	}
	return endpoint, nil
}

func addressed() fakeAddresses {
	return fakeAddresses{endpoints: map[string]Endpoint{
		"i-1":         {NodeID: "n-1", Address: "10.20.0.65"},
		"i-2":         {NodeID: "n-1", Address: "10.20.0.66"},
		"i-elsewhere": {NodeID: "n-2", Address: "10.20.0.130"},
		"i-pending":   {},
	}}
}

func newTestModule(t *testing.T, addresses Addresses) (http.Handler, *Module) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, addresses, logging.New("error", io.Discard))
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

func publish(t *testing.T, h http.Handler, body string) response {
	t.Helper()

	rec := request(t, h, http.MethodPost, "/v1/forwards", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func TestPublishingAPortRecordsWhereItLands(t *testing.T) {
	h, _ := newTestModule(t, addressed())

	created := publish(t, h, `{"instance_id":"i-1","target_port":80,"node_port":8080}`)

	if created.NodeID != "n-1" || created.Address != "10.20.0.65" {
		t.Fatalf("forward = %+v, want the node and address of the instance", created)
	}
	if created.Protocol != ProtocolTCP {
		t.Fatalf("protocol = %q, want tcp by default", created.Protocol)
	}
}

func TestTheNodePortDefaultsToTheTargetPort(t *testing.T) {
	h, _ := newTestModule(t, addressed())

	created := publish(t, h, `{"instance_id":"i-1","target_port":8443}`)
	if created.NodePort != 8443 {
		t.Fatalf("node_port = %d, want it to mirror the target", created.NodePort)
	}
}

func TestOneNodePortHasOneOwner(t *testing.T) {
	h, _ := newTestModule(t, addressed())

	publish(t, h, `{"instance_id":"i-1","target_port":80,"node_port":8080}`)

	rec := request(t, h, http.MethodPost, "/v1/forwards",
		`{"instance_id":"i-2","target_port":80,"node_port":8080}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: two dnat rules for one port is ambiguous",
			rec.Code, http.StatusConflict)
	}

	if rec := request(t, h, http.MethodPost, "/v1/forwards",
		`{"instance_id":"i-elsewhere","target_port":80,"node_port":8080}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want the same port free on another node", rec.Code)
	}

	if rec := request(t, h, http.MethodPost, "/v1/forwards",
		`{"instance_id":"i-2","target_port":80,"node_port":8080,"protocol":"udp"}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want tcp and udp to be separate ports", rec.Code)
	}
}

func TestAPrivilegedNodePortIsFineBecauseNothingListens(t *testing.T) {
	h, _ := newTestModule(t, addressed())

	created := publish(t, h, `{"instance_id":"i-1","target_port":8080,"node_port":80}`)
	if created.NodePort != 80 {
		t.Fatalf("node_port = %d, want 80: dnat rewrites a packet, it does not bind a socket",
			created.NodePort)
	}
}

func TestBadForwardsAreRejected(t *testing.T) {
	h, _ := newTestModule(t, addressed())

	cases := map[string]struct {
		body string
		want int
	}{
		"no instance":         {`{"target_port":80}`, http.StatusBadRequest},
		"no target port":      {`{"instance_id":"i-1"}`, http.StatusBadRequest},
		"target out of range": {`{"instance_id":"i-1","target_port":70000}`, http.StatusBadRequest},
		"node port out of range": {
			`{"instance_id":"i-1","target_port":80,"node_port":70000}`, http.StatusBadRequest},
		"unknown protocol": {
			`{"instance_id":"i-1","target_port":80,"protocol":"sctp"}`, http.StatusBadRequest},
		"unknown instance": {`{"instance_id":"i-nope","target_port":80}`, http.StatusNotFound},
		"instance with no address": {
			`{"instance_id":"i-pending","target_port":80}`, http.StatusConflict},
		"unknown field": {
			`{"instance_id":"i-1","target_port":80,"host_port":8080}`, http.StatusBadRequest},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := request(t, h, http.MethodPost, "/v1/forwards", want.body); rec.Code != want.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, want.want, rec.Body.String())
			}
		})
	}
}

func TestANodeOnlySeesItsOwnForwards(t *testing.T) {
	h, _ := newTestModule(t, addressed())

	publish(t, h, `{"instance_id":"i-1","target_port":80,"node_port":8080}`)
	publish(t, h, `{"instance_id":"i-elsewhere","target_port":80,"node_port":9090}`)

	var list listResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/nodes/n-1/forwards", "").Body.Bytes(), &list,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Forwards) != 1 || list.Forwards[0].NodePort != 8080 {
		t.Fatalf("forwards = %+v, want only the rule this node must program", list.Forwards)
	}
}

func TestDeletingAnInstanceUnpublishesItsPorts(t *testing.T) {
	h, m := newTestModule(t, addressed())

	publish(t, h, `{"instance_id":"i-1","target_port":80,"node_port":8080}`)
	publish(t, h, `{"instance_id":"i-1","target_port":443,"node_port":8443}`)
	publish(t, h, `{"instance_id":"i-2","target_port":80,"node_port":9080}`)

	if err := m.ReleaseInstance(context.Background(), "i-1"); err != nil {
		t.Fatalf("release: %v", err)
	}

	var list listResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/forwards", "").Body.Bytes(), &list,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Forwards) != 1 || list.Forwards[0].InstanceID != "i-2" {
		t.Fatalf("forwards = %+v, want a deleted instance to take its rules with it", list.Forwards)
	}
}

func TestDeletingAForward(t *testing.T) {
	h, _ := newTestModule(t, addressed())

	created := publish(t, h, `{"instance_id":"i-1","target_port":80,"node_port":8080}`)

	if rec := request(t, h, http.MethodDelete, "/v1/forwards/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec := request(t, h, http.MethodDelete, "/v1/forwards/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d on a second delete", rec.Code)
	}
}
