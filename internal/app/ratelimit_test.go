package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

func newLimitedApp(t *testing.T, perSecond, burst int) *testApp {
	t.Helper()

	dir := schemaDir(t)
	built, err := New(context.Background(), Config{
		DataDir:       dir,
		RatePerSecond: &perSecond,
		RateBurst:     burst,
	}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { built.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}
	return &testApp{App: built, secret: strings.TrimSpace(string(raw))}
}

func TestACallerPastItsBurstIsRefused(t *testing.T) {
	a := newLimitedApp(t, 1, 3)

	for i := range 3 {
		if rec := do(t, a, http.MethodGet, "/v1/version", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, a, http.MethodGet, "/v1/version", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After, so a client can only guess when to come back")
	}
	if !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("body = %s, want a code a client can branch on", rec.Body.String())
	}
}

func TestOneTokenCannotSpendAnother(t *testing.T) {
	a := newLimitedApp(t, 1, 2)
	other := tokenIn(t, a, "second", newProject(t, a, "other"))

	for range 4 {
		do(t, a, http.MethodGet, "/v1/version", nil)
	}
	if rec := do(t, a, http.MethodGet, "/v1/version", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the first token was not limited: %d", rec.Code)
	}

	rec := doAs(t, a, other, http.MethodGet, "/v1/version", nil)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("a second token was refused because the first one was loud, so one noisy " +
			"client takes the whole platform down")
	}
}

func TestHealthIsNeverRateLimited(t *testing.T) {
	a := newLimitedApp(t, 1, 2)

	for range 20 {
		do(t, a, http.MethodGet, "/v1/version", nil)
	}

	for i := range 20 {
		rec := do(t, a, http.MethodGet, "/healthz", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("healthz %d: status = %d, want %d: throttling the health check makes a "+
				"busy platform look like a dead one", i, rec.Code, http.StatusOK)
		}
	}
}

func TestAFloodOfBadTokensIsLimitedTooAndDoesNotReachTheDatabase(t *testing.T) {
	a := newLimitedApp(t, 1, 3)

	var refused int
	for i := range 10 {
		rec := doAs(t, a, "wrong-secret", http.MethodGet, "/v1/version", nil)
		if rec.Code == http.StatusTooManyRequests {
			refused++
		} else if rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: status = %d, want 401 or 429", i, rec.Code)
		}
	}
	if refused == 0 {
		t.Fatal("an unauthenticated flood was never limited, so every bogus token costs a " +
			"lookup on the single sqlite connection")
	}
}

func TestNoLimitConfiguredAcceptsEverything(t *testing.T) {
	a := newLimitedApp(t, 0, 0)

	for i := range 50 {
		if rec := do(t, a, http.MethodGet, "/v1/version", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d with the limit off", i, rec.Code)
		}
	}
}
