package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

func newConsoleApp(t *testing.T) (*testApp, string) {
	t.Helper()

	dir := schemaDir(t)
	console := t.TempDir()

	write := func(name, body string) {
		full := filepath.Join(console, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("make %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write("index.html", "<!doctype html><title>console</title>")
	write("meridian.css", ":root{--ms-ink:#111}")
	write("assets/deep.txt", "nested")

	log := logging.New("error", io.Discard)
	a, err := New(context.Background(), Config{
		DataDir:       dir,
		RatePerSecond: &unlimited,
		ConsoleDir:    console,
	}, log)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}
	return &testApp{App: a, secret: strings.TrimSpace(string(raw))}, console
}

func fetch(t *testing.T, a *testApp, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func TestTheConsoleIsServedWithoutATokenBecauseItOnlyOffersASignIn(t *testing.T) {
	a, _ := newConsoleApp(t)

	rec := fetch(t, a, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d, want the console to load before anyone has signed in", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<title>console</title>") {
		t.Fatalf("GET / served %q, want index.html", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("the console was served without a content security policy")
	}
}

func TestTheConsoleNeverShadowsTheAPI(t *testing.T) {
	a, _ := newConsoleApp(t)

	anonymous := fetch(t, a, http.MethodGet, "/v1/instances")
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /v1/instances: %d, want %d: serving a console must not "+
			"turn an API path into a static file", anonymous.Code, http.StatusUnauthorized)
	}

	held := do(t, a, http.MethodGet, "/v1/instances", nil)
	if held.Code != http.StatusOK {
		t.Fatalf("GET /v1/instances with a token: %d, want %d", held.Code, http.StatusOK)
	}

	health := fetch(t, a, http.MethodGet, "/healthz")
	if health.Code != http.StatusOK {
		t.Fatalf("GET /healthz: %d, want %d", health.Code, http.StatusOK)
	}
}

func TestNothingIsServedAtTheRootWithoutAConsoleDirectory(t *testing.T) {
	a := newTestApp(t)

	rec := fetch(t, a, http.MethodGet, "/")
	if rec.Code == http.StatusOK {
		t.Fatal("GET / answered 200 with no console configured, so a control plane that " +
			"was never asked to serve a console is serving something")
	}
}

func TestTheConsoleRefusesToReachOutsideItsDirectory(t *testing.T) {
	a, console := newConsoleApp(t)

	secret := filepath.Join(filepath.Dir(console), "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatalf("write the file next door: %v", err)
	}

	for _, path := range []string{
		"/../secret.txt",
		"/..%2Fsecret.txt",
		"/assets/../../secret.txt",
		"/./../secret.txt",
	} {
		rec := fetch(t, a, http.MethodGet, path)
		if strings.Contains(rec.Body.String(), "not yours") {
			t.Fatalf("GET %s served a file from outside the console directory", path)
		}
	}
}

func TestADirectoryIsNotSomethingTheConsoleServes(t *testing.T) {
	a, _ := newConsoleApp(t)

	for _, path := range []string{"/assets", "/assets/"} {
		rec := fetch(t, a, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: %d, want %d: a directory is not a file, and answering "+
				"anything else means the reply is whatever reading one produced",
				path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestTheConsoleOnlyAnswersReads(t *testing.T) {
	a, _ := newConsoleApp(t)

	rec := fetch(t, a, http.MethodPost, "/")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestABrowserCarriesItsSessionInACookieItCannotRead(t *testing.T) {
	a, _ := newConsoleApp(t)
	someone(t, a, "ada@example.test", "member")

	body := `{"email":"ada@example.test","password":"` + goodPassword + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("login: %d", rec.Code)
	}

	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == httpx.SessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("login set no %s cookie, so a browser has nowhere to keep the session",
			httpx.SessionCookie)
	}
	if !session.HttpOnly {
		t.Fatal("the session cookie is readable by scripts, so one injected script in the " +
			"console is a stolen session")
	}
	if session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie SameSite = %v, want strict, or another site can spend it",
			session.SameSite)
	}

	authed := httptest.NewRequest(http.MethodGet, "/v1/instances", nil)
	authed.AddCookie(session)
	got := httptest.NewRecorder()
	a.Handler().ServeHTTP(got, authed)
	if got.Code != http.StatusOK {
		t.Fatalf("GET /v1/instances with only the cookie: %d, want %d", got.Code, http.StatusOK)
	}
}

func TestSigningOutStopsTheSessionItWasHolding(t *testing.T) {
	a, _ := newConsoleApp(t)
	someone(t, a, "ada@example.test", "member")

	code, session := login(t, a, "ada@example.test", goodPassword)
	if code != http.StatusCreated {
		t.Fatalf("login: %d", code)
	}

	out := doAs(t, a, session.Token, http.MethodPost, "/v1/logout", nil)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout: %d, want %d", out.Code, http.StatusNoContent)
	}

	cleared := false
	for _, c := range out.Result().Cookies() {
		if c.Name == httpx.SessionCookie && c.Value == "" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout left the session cookie in place")
	}

	again := doAs(t, a, session.Token, http.MethodGet, "/v1/instances", nil)
	if again.Code != http.StatusUnauthorized {
		t.Fatalf("the token still worked after signing out: %d, want %d",
			again.Code, http.StatusUnauthorized)
	}
}
