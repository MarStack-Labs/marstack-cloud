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

func newProjectLimitedApp(t *testing.T, perSecond, burst int) *testApp {
	t.Helper()

	dir := schemaDir(t)
	built, err := New(context.Background(), Config{
		DataDir:          dir,
		RatePerSecond:    &unlimited,
		ProjectPerSecond: &perSecond,
		ProjectBurst:     burst,
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

func TestTwoTokensInOneProjectShareItsBudget(t *testing.T) {
	a := newProjectLimitedApp(t, 1, 4)

	second := tokenIn(t, a, "second", "prj-default")

	spent := false
	for range 20 {
		if do(t, a, http.MethodGet, "/v1/version", nil).Code == http.StatusTooManyRequests {
			spent = true
			break
		}
	}
	if !spent {
		t.Fatal("the first token never ran out of budget")
	}

	rec := doAs(t, a, second, http.MethodGet, "/v1/version", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: a second token in the same project must draw on the "+
			"same budget, or the limit is per token again", rec.Code, http.StatusTooManyRequests)
	}
}

func TestAnotherProjectHasItsOwnBudget(t *testing.T) {
	a := newProjectLimitedApp(t, 1, 3)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))

	for range 5 {
		do(t, a, http.MethodGet, "/v1/version", nil)
	}
	if rec := do(t, a, http.MethodGet, "/v1/version", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the first project was not limited: %d", rec.Code)
	}

	rec := doAs(t, a, other, http.MethodGet, "/v1/version", nil)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("another project was refused because the first one was busy, which is the " +
			"noisy-neighbour problem this exists to stop")
	}
}

func TestHealthIsNotProjectLimitedEither(t *testing.T) {
	a := newProjectLimitedApp(t, 1, 2)

	for range 20 {
		do(t, a, http.MethodGet, "/v1/version", nil)
	}

	for i := range 10 {
		if rec := do(t, a, http.MethodGet, "/healthz", nil); rec.Code != http.StatusOK {
			t.Fatalf("healthz %d: %d", i, rec.Code)
		}
	}
}

func TestNoProjectLimitConfiguredAcceptsEverything(t *testing.T) {
	a := newProjectLimitedApp(t, 0, 0)

	for i := range 50 {
		if rec := do(t, a, http.MethodGet, "/v1/version", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
}
