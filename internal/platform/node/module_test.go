package node

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

type harness struct {
	mux http.Handler
	now *time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard))
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	clockNow := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	h := &harness{now: &clockNow}

	m.handler.svc.now = func() time.Time { return *h.now }

	mux := http.NewServeMux()
	m.Routes(mux)
	h.mux = mux
	return h
}

func (h *harness) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
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
	h.mux.ServeHTTP(rec, req)
	return rec
}

func decodeNode(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()

	var got response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode node: %v (body: %s)", err, rec.Body.String())
	}
	return got
}

const registerBody = `{"name":"bm-1","zone":"rack-a","arch":"arm64","os":"linux","cpus":4,"memory_mib":6144,"agent_version":"0.0.1-dev"}`

func TestRegisterCreatesNode(t *testing.T) {
	h := newHarness(t)

	rec := h.request(t, http.MethodPost, "/v1/nodes/register", registerBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	got := decodeNode(t, rec)
	if !strings.HasPrefix(got.ID, "n-") {
		t.Errorf("id = %q, want an n- prefixed id", got.ID)
	}
	if got.Status != string(StatusReady) {
		t.Errorf("status = %q, want %q", got.Status, StatusReady)
	}
	if got.Zone != "rack-a" || got.CPUs != 4 {
		t.Errorf("node = %+v, want zone rack-a and 4 cpus", got)
	}
}

func TestRegisterIsIdempotentForTheSameName(t *testing.T) {
	h := newHarness(t)

	first := decodeNode(t, h.request(t, http.MethodPost, "/v1/nodes/register", registerBody))
	second := decodeNode(t, h.request(t, http.MethodPost, "/v1/nodes/register", registerBody))

	if first.ID != second.ID {
		t.Fatalf("second register produced id %q, want the original %q", second.ID, first.ID)
	}

	var list listResponse
	if err := json.Unmarshal(h.request(t, http.MethodGet, "/v1/nodes", "").Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(list.Nodes))
	}
}

func TestRegisterUpdatesChangedCapacity(t *testing.T) {
	h := newHarness(t)
	h.request(t, http.MethodPost, "/v1/nodes/register", registerBody)

	grown := `{"name":"bm-1","zone":"rack-a","arch":"arm64","os":"linux","cpus":8,"memory_mib":16384,"agent_version":"0.0.2"}`
	got := decodeNode(t, h.request(t, http.MethodPost, "/v1/nodes/register", grown))

	if got.CPUs != 8 || got.MemoryMiB != 16384 {
		t.Fatalf("node = %+v, want the grown capacity", got)
	}
	if got.AgentVersion != "0.0.2" {
		t.Fatalf("agent_version = %q, want 0.0.2", got.AgentVersion)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"empty name", `{"name":"","arch":"arm64","os":"linux","cpus":4,"memory_mib":6144}`, "invalid_name"},
		{"bad zone", `{"name":"bm-1","zone":"Rack A","arch":"arm64","os":"linux","cpus":4,"memory_mib":6144}`, "invalid_zone"},
		{"no arch", `{"name":"bm-1","arch":"","os":"linux","cpus":4,"memory_mib":6144}`, "invalid_arch"},
		{"zero cpus", `{"name":"bm-1","arch":"arm64","os":"linux","cpus":0,"memory_mib":6144}`, "invalid_cpus"},
		{"tiny memory", `{"name":"bm-1","arch":"arm64","os":"linux","cpus":4,"memory_mib":8}`, "invalid_memory"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newHarness(t).request(t, http.MethodPost, "/v1/nodes/register", tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			json.Unmarshal(rec.Body.Bytes(), &body)
			if body.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %q", body.Error.Code, tc.wantCode)
			}
		})
	}
}

func TestNodeGoesUnreachableWithoutHeartbeat(t *testing.T) {
	h := newHarness(t)
	created := decodeNode(t, h.request(t, http.MethodPost, "/v1/nodes/register", registerBody))

	*h.now = h.now.Add(ReadyWindow + time.Second)

	stale := decodeNode(t, h.request(t, http.MethodGet, "/v1/nodes/"+created.ID, ""))
	if stale.Status != string(StatusUnreachable) {
		t.Fatalf("status = %q, want %q", stale.Status, StatusUnreachable)
	}

	revived := decodeNode(t, h.request(t, http.MethodPost, "/v1/nodes/"+created.ID+"/heartbeat", ""))
	if revived.Status != string(StatusReady) {
		t.Fatalf("status after heartbeat = %q, want %q", revived.Status, StatusReady)
	}
}

func TestHeartbeatForUnknownNodeIsNotFound(t *testing.T) {
	rec := newHarness(t).request(t, http.MethodPost, "/v1/nodes/n-missing/heartbeat", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "node_not_found" {
		t.Fatalf("error code = %q, want node_not_found", body.Error.Code)
	}
}

func TestListReturnsEmptyArrayNotNull(t *testing.T) {
	rec := newHarness(t).request(t, http.MethodGet, "/v1/nodes", "")

	if !strings.Contains(rec.Body.String(), `"nodes": []`) {
		t.Fatalf("body = %s, want an empty nodes array", rec.Body.String())
	}
}
