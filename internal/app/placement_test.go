package app

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
)

func newSchedulingApp(t *testing.T) *App {
	t.Helper()

	a, err := New(context.Background(), Config{
		DataDir:           t.TempDir(),
		SchedulerInterval: 10 * time.Millisecond,
	}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func post(t *testing.T, a *App, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func instanceNodeID(t *testing.T, a *App, id string) string {
	t.Helper()

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/instances/"+id, nil))

	var body struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode instance: %v", err)
	}
	return body.NodeID
}

func waitForPlacement(t *testing.T, a *App, instanceID string) string {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if nodeID := instanceNodeID(t, a, instanceID); nodeID != "" {
			return nodeID
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

func TestSchedulerPlacesInstanceOnRegisteredNode(t *testing.T) {
	a := newSchedulingApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.scheduler.Run(ctx)

	var created struct {
		ID string `json:"id"`
	}
	rec := post(t, a, "/v1/instances", `{"name":"api-1","isolation":"container","image":"alpine:3.20","command":["/bin/sh","-c","sleep 100"]}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created instance: %v", err)
	}

	if nodeID := instanceNodeID(t, a, created.ID); nodeID != "" {
		t.Fatalf("node_id = %q, want it empty while no node is registered", nodeID)
	}

	var registered struct {
		ID string `json:"id"`
	}
	rec = post(t, a, "/v1/nodes/register",
		`{"name":"bm-1","zone":"rack-a","arch":"arm64","os":"linux","cpus":4,"memory_mib":6144,"agent_version":"test"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatalf("decode registered node: %v", err)
	}

	placed := waitForPlacement(t, a, created.ID)
	if placed != registered.ID {
		t.Fatalf("node_id = %q, want the instance placed on %q", placed, registered.ID)
	}
}

func TestPlacementIsNotUndoneByAFurtherPass(t *testing.T) {
	a := newSchedulingApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.scheduler.Run(ctx)

	post(t, a, "/v1/nodes/register",
		`{"name":"bm-1","arch":"arm64","os":"linux","cpus":4,"memory_mib":6144,"agent_version":"test"}`)

	var created struct {
		ID string `json:"id"`
	}
	rec := post(t, a, "/v1/instances", `{"name":"api-1","isolation":"container","image":"alpine:3.20","command":["/bin/sh","-c","sleep 100"]}`)
	json.Unmarshal(rec.Body.Bytes(), &created)

	first := waitForPlacement(t, a, created.ID)
	if first == "" {
		t.Fatal("instance was never placed")
	}

	time.Sleep(100 * time.Millisecond)

	if again := instanceNodeID(t, a, created.ID); again != first {
		t.Fatalf("node_id = %q, want it to stay %q across scheduling passes", again, first)
	}
}
