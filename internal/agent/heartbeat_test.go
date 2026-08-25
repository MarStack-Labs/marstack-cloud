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
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

type slowRuntime struct {
	fakeRuntime
	block time.Duration
}

func (s *slowRuntime) Status(ctx context.Context, instanceID string) (workload.State, error) {
	time.Sleep(s.block)
	return s.fakeRuntime.Status(ctx, instanceID)
}

func TestHeartbeatKeepsFlowingWhileReconcileIsBusy(t *testing.T) {
	var (
		mu         sync.Mutex
		heartbeats int
	)

	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/nodes/register", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc", Name: "bm-1"})
	})
	mux.HandleFunc("POST /v1/nodes/{id}/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		heartbeats++
		mu.Unlock()
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc"})
	})
	mux.Handle("/", cp.handler())

	srv := httptest.NewServer(mux)
	defer srv.Close()

	runtime := &slowRuntime{
		fakeRuntime: fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}},
		block:       400 * time.Millisecond,
	}

	a := New(Config{
		Endpoint:          srv.URL,
		Name:              "bm-1",
		Interval:          20 * time.Millisecond,
		HeartbeatInterval: 20 * time.Millisecond,
	}, Deps{Runtimes: runtimesFor(runtime)}, logging.New("error", io.Discard))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := a.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	mu.Lock()
	got := heartbeats
	mu.Unlock()

	if got < 5 {
		t.Fatalf("heartbeats = %d, want many: a slow reconcile must not make a working node look dead", got)
	}
}
