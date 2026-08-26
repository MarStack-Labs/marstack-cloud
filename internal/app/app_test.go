package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

type testApp struct {
	*App
	secret string
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()

	dir := t.TempDir()
	log := logging.New("error", io.Discard)
	a, err := New(context.Background(), Config{DataDir: dir}, log)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}
	return &testApp{App: a, secret: strings.TrimSpace(string(raw))}
}

func do(t *testing.T, a *testApp, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+a.secret)

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func doAs(t *testing.T, a *testApp, secret, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, body)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	rec := do(t, newTestApp(t), http.MethodGet, "/healthz", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %q, want %q", body["status"], "ok")
	}
}

func TestVersionEndpoint(t *testing.T) {
	rec := do(t, newTestApp(t), http.MethodGet, "/v1/version", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["version"] == "" {
		t.Fatal("version field is empty")
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	rec := do(t, newTestApp(t), http.MethodGet, "/nope", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHealthzReportsUnavailableWhenStoreClosed(t *testing.T) {
	a := newTestApp(t)
	if err := a.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	rec := do(t, a, http.MethodGet, "/healthz", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(rec.Body.String(), "sql:") {
		t.Fatalf("response leaks internal detail: %s", rec.Body.String())
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	rec := do(t, newTestApp(t), http.MethodGet, "/healthz", nil)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
}

func TestRequestIDHeaderIsSet(t *testing.T) {
	rec := do(t, newTestApp(t), http.MethodGet, "/healthz", nil)

	if id := rec.Header().Get("X-Request-Id"); !strings.HasPrefix(id, "req-") {
		t.Fatalf("X-Request-Id = %q, want a req- prefixed id", id)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	log := logging.New("error", io.Discard)

	for range 3 {
		a, err := New(context.Background(), Config{DataDir: dir}, log)
		if err != nil {
			t.Fatalf("new app: %v", err)
		}
		a.Close()
	}
}
