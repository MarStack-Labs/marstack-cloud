package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type challengeServer struct {
	scheme     string
	tokenAuth  string
	apiAuth    string
	tokenCalls int
}

func (c *challengeServer) handler(t *testing.T) http.Handler {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		c.tokenCalls++
		c.tokenAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "issued-token"})
	})

	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if held := r.Header.Get("Authorization"); held != "" {
			c.apiAuth = held
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
			return
		}
		if c.scheme == "basic" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		} else {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="`+baseOf(r)+`/token",service="registry"`)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	return mux
}

func baseOf(r *http.Request) string {
	return "http://" + r.Host
}

func newTestRegistry(t *testing.T, held ...Credential) (*registry, *challengeServer, string) {
	t.Helper()

	cp := &challengeServer{}
	srv := httptest.NewServer(cp.handler(t))
	t.Cleanup(srv.Close)

	r := newRegistry(true)
	if len(held) > 0 {
		for i := range held {
			held[i].Host = strings.TrimPrefix(srv.URL, "http://")
		}
		r.useCredentials(held)
	}
	return r, cp, strings.TrimPrefix(srv.URL, "http://")
}

func TestTheTokenRequestCarriesTheLogin(t *testing.T) {
	r, cp, host := newTestRegistry(t, Credential{Username: "marstack", Password: "s3cret"})

	res, err := r.get(context.Background(),
		Reference{Registry: host, Repository: "team/app", Tag: "v1"}, "/manifests/v1", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	res.Body.Close()

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("marstack:s3cret"))
	if cp.tokenAuth != want {
		t.Fatalf("token request sent %q, want the login: an anonymous token request gets an "+
			"anonymous token, which is exactly the pull that was already failing",
			cp.tokenAuth)
	}
	if cp.apiAuth != "Bearer issued-token" {
		t.Fatalf("api request sent %q, want the issued token", cp.apiAuth)
	}
}

func TestABasicOnlyRegistryIsAnswered(t *testing.T) {
	r, cp, host := newTestRegistry(t, Credential{Username: "u", Password: "p"})
	cp.scheme = "basic"

	res, err := r.get(context.Background(),
		Reference{Registry: host, Repository: "team/app", Tag: "v1"}, "/manifests/v1", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	res.Body.Close()

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	if cp.apiAuth != want {
		t.Fatalf("api request sent %q, want basic auth: a self hosted registry that asks for "+
			"Basic was refused outright before this", cp.apiAuth)
	}
	if cp.tokenCalls != 0 {
		t.Fatalf("the token endpoint was called %d times for a Basic challenge", cp.tokenCalls)
	}
}

func TestABasicChallengeWithNoLoginSaysSo(t *testing.T) {
	r, cp, host := newTestRegistry(t)
	cp.scheme = "basic"

	_, err := r.get(context.Background(),
		Reference{Registry: host, Repository: "team/app", Tag: "v1"}, "/manifests/v1", nil)
	if err == nil {
		t.Fatal("a registry asking for a password was answered without one")
	}
	if !strings.Contains(err.Error(), "holds none") {
		t.Fatalf("error = %v, want it to say this node has no credential for that host", err)
	}
}

func TestChangingALoginThrowsAwayTheOldToken(t *testing.T) {
	r, cp, host := newTestRegistry(t, Credential{Username: "one", Password: "a"})
	ref := Reference{Registry: host, Repository: "team/app", Tag: "v1"}

	res, err := r.get(context.Background(), ref, "/manifests/v1", nil)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	res.Body.Close()

	r.useCredentials([]Credential{{Host: host, Username: "two", Password: "b"}})

	res, err = r.get(context.Background(), ref, "/manifests/v1", nil)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	res.Body.Close()

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("two:b"))
	if cp.tokenAuth != want {
		t.Fatalf("token request sent %q after the login changed, want the new one: a cached "+
			"token outliving the credential it came from is a rotation that does nothing",
			cp.tokenAuth)
	}
	if cp.tokenCalls != 2 {
		t.Fatalf("the token endpoint was called %d times, want one per login", cp.tokenCalls)
	}
}

func TestAnUnchangedLoginKeepsItsToken(t *testing.T) {
	r, cp, host := newTestRegistry(t, Credential{Username: "one", Password: "a"})
	ref := Reference{Registry: host, Repository: "team/app", Tag: "v1"}

	res, err := r.get(context.Background(), ref, "/manifests/v1", nil)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	res.Body.Close()

	r.useCredentials([]Credential{{Host: host, Username: "one", Password: "a"}})

	res, err = r.get(context.Background(), ref, "/manifests/v1", nil)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	res.Body.Close()

	if cp.tokenCalls != 1 {
		t.Fatalf("the token endpoint was called %d times, want one: the agent hands the same "+
			"credentials down every reconcile pass, so re-authenticating on each one is a "+
			"round trip per pull for nothing", cp.tokenCalls)
	}
}
