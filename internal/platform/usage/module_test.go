package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestSamplesInDifferentMinutesLandInDifferentBuckets(t *testing.T) {
	h, m := newTestModule(t)

	clock := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	m.svc.now = func() time.Time { return clock }

	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":10,"memory_used_mib":100,"memory_mib":6000}`)

	clock = clock.Add(30 * time.Second)
	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":20,"memory_used_mib":200,"memory_mib":6000}`)

	clock = clock.Add(40 * time.Second)
	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":90,"memory_used_mib":300,"memory_mib":6000}`)

	found, err := m.svc.historyOf(context.Background(), "n-1", time.Hour)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(found.Buckets) != 2 {
		t.Fatalf("buckets = %d, want two: the first minute held two samples and the second one",
			len(found.Buckets))
	}
	if found.Buckets[0].Samples != 2 || found.Buckets[0].CPUPeak != 20 {
		t.Fatalf("first = %+v, want two samples peaking at 20", found.Buckets[0])
	}
	if found.Buckets[1].Samples != 1 || found.Buckets[1].CPUPeak != 90 {
		t.Fatalf("second = %+v, want one sample at 90", found.Buckets[1])
	}
	if !found.Buckets[1].At.After(found.Buckets[0].At) {
		t.Fatal("buckets came back out of order")
	}
}

func TestAWindowOnlyReachesBackAsFarAsAsked(t *testing.T) {
	h, m := newTestModule(t)

	clock := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	m.svc.now = func() time.Time { return clock }

	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":10,"memory_used_mib":100,"memory_mib":6000}`)

	clock = clock.Add(2 * time.Hour)
	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":20,"memory_used_mib":200,"memory_mib":6000}`)

	recent, err := m.svc.historyOf(context.Background(), "n-1", 30*time.Minute)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(recent.Buckets) != 1 {
		t.Fatalf("buckets = %d, want only the one inside the window", len(recent.Buckets))
	}

	all, err := m.svc.historyOf(context.Background(), "n-1", 3*time.Hour)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(all.Buckets) != 2 {
		t.Fatalf("buckets = %d, want both with a wider window", len(all.Buckets))
	}
}

func TestAWindowIsCappedRatherThanRefused(t *testing.T) {
	h, m := newTestModule(t)

	clock := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	m.svc.now = func() time.Time { return clock }
	request(t, h, http.MethodPut, "/v1/nodes/n-1/usage",
		`{"cpu_percent":10,"memory_used_mib":100,"memory_mib":6000}`)

	if _, err := m.svc.historyOf(context.Background(), "n-1", 400*time.Hour); err != nil {
		t.Fatalf("a window past the retention should be capped, not refused: %v", err)
	}
}

func TestOldBucketsArePruned(t *testing.T) {
	_, m := newTestModule(t)
	ctx := context.Background()

	clock := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	m.svc.now = func() time.Time { return clock }

	report := func() {
		m.svc.record(ctx, Report{Node: NodeSample{
			NodeID: "n-1", CPUPercent: 5, MemoryUsedMiB: 100, MemoryMiB: 6000,
		}})
	}

	report()
	oldest := bucketOf(clock)

	clock = clock.Add((HistoryBuckets + 10) * BucketSize)
	for range PruneEvery {
		report()
	}

	buckets, err := m.svc.repo.history(ctx, "n-1", 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, bucket := range buckets {
		if bucketOf(bucket.At) == oldest {
			t.Fatal("a bucket older than the retention window survived, so the table grows " +
				"without bound")
		}
	}
	if len(buckets) == 0 {
		t.Fatal("pruning took everything, including the buckets inside the window")
	}
}
