package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) record(method, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, method+" "+path)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recorder) count(call string) int {
	n := 0
	for _, c := range r.snapshot() {
		if c == call {
			n++
		}
	}
	return n
}

func newTestAgent(t *testing.T, endpoint string, interval time.Duration) *Agent {
	t.Helper()
	return New(Config{
		Endpoint: endpoint,
		Name:     "bm-1",
		Zone:     "rack-a",
		Interval: interval,
	}, Deps{}, logging.New("error", io.Discard))
}

func TestAgentRegistersThenHeartbeats(t *testing.T) {
	rec := &recorder{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc", Name: "bm-1", Zone: "rack-a", Status: "ready"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if err := newTestAgent(t, srv.URL, 20*time.Millisecond).Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := rec.count("POST /v1/nodes/register"); got != 1 {
		t.Errorf("register calls = %d, want exactly 1", got)
	}
	if got := rec.count("POST /v1/nodes/n-abc/heartbeat"); got < 2 {
		t.Errorf("heartbeat calls = %d, want at least 2", got)
	}
}

func TestAgentSendsHostFactsOnRegister(t *testing.T) {
	var body registerBody

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			json.NewDecoder(r.Body).Decode(&body)
		}
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	newTestAgent(t, srv.URL, time.Hour).Run(ctx)

	if body.Name != "bm-1" || body.Zone != "rack-a" {
		t.Errorf("body = %+v, want the configured name and zone", body)
	}
	if body.Arch == "" || body.OS == "" || body.CPUs < 1 {
		t.Errorf("body = %+v, want host facts to be filled in", body)
	}
	if body.AgentVersion == "" {
		t.Error("agent_version is empty; the control plane cannot tell agents apart")
	}
}

func TestAgentRetriesWhenControlPlaneIsDown(t *testing.T) {
	rec := &recorder{}
	var attempts int
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)

		mu.Lock()
		attempts++
		failing := attempts <= 2
		mu.Unlock()

		if failing {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"code":"store_unavailable","message":"nope"}}`))
			return
		}
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc"})
	}))
	defer srv.Close()

	a := newTestAgent(t, srv.URL, time.Hour)
	a.log = logging.New("error", io.Discard)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	if err := a.registerWithRetry(ctx); err != nil {
		t.Fatalf("registerWithRetry: %v", err)
	}
	if a.nodeID != "n-abc" {
		t.Fatalf("node id = %q, want n-abc after the retries succeed", a.nodeID)
	}
	if got := rec.count("POST /v1/nodes/register"); got != 3 {
		t.Fatalf("register calls = %d, want 3 (two failures then success)", got)
	}
}

func TestAgentGivesUpOnRejectedRegistration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"invalid_name","message":"bad name"}}`))
	}))
	defer srv.Close()

	err := newTestAgent(t, srv.URL, time.Hour).registerWithRetry(context.Background())
	if err == nil {
		t.Fatal("expected an error: a rejected registration will never succeed by retrying")
	}
}

func TestAgentReregistersWhenControlPlaneForgetsIt(t *testing.T) {
	rec := &recorder{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method, r.URL.Path)
		if r.URL.Path == "/v1/nodes/n-abc/heartbeat" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"node_not_found","message":"gone"}}`))
			return
		}
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc"})
	}))
	defer srv.Close()

	a := newTestAgent(t, srv.URL, time.Hour)
	ctx := context.Background()

	if err := a.register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}

	a.tick(ctx)
	if a.nodeID != "" {
		t.Fatalf("node id = %q, want it cleared so the next tick registers again", a.nodeID)
	}

	a.tick(ctx)
	if a.nodeID != "n-abc" {
		t.Fatalf("node id = %q, want the agent to have registered again", a.nodeID)
	}
	if got := rec.count("POST /v1/nodes/register"); got != 2 {
		t.Fatalf("register calls = %d, want 2", got)
	}
}

func TestInspectHostReportsUsableFacts(t *testing.T) {
	host := inspectHost()

	if host.Arch == "" || host.OS == "" {
		t.Errorf("host = %+v, want arch and os", host)
	}
	if host.CPUs < 1 {
		t.Errorf("cpus = %d, want at least 1", host.CPUs)
	}
}
