package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func nodeToken(t *testing.T, a *testApp) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"bm-1","role":"node"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create a node token: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Secret == "" {
		t.Fatal("the secret was not returned, so nobody could ever use the token")
	}
	return created.Secret
}

func TestHealthzNeedsNoToken(t *testing.T) {
	a := newTestApp(t)

	if rec := doAs(t, a, "", http.MethodGet, "/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want a liveness probe to work without credentials", rec.Code)
	}
}

func TestEveryOtherEndpointNeedsAToken(t *testing.T) {
	a := newTestApp(t)

	for _, path := range []string{"/v1/version", "/v1/instances", "/v1/nodes", "/v1/tokens"} {
		t.Run(path, func(t *testing.T) {
			if rec := doAs(t, a, "", http.MethodGet, path, nil); rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAnUnknownTokenIsRefused(t *testing.T) {
	a := newTestApp(t)

	rec := doAs(t, a, "mst_notarealtoken", http.MethodGet, "/v1/instances", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "unknown_token" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestANodeTokenReachesOnlyWhatAnAgentNeeds(t *testing.T) {
	a := newTestApp(t)
	secret := nodeToken(t, a)

	allowed := map[string]string{
		http.MethodGet: "/v1/nodes",
	}
	for method, path := range allowed {
		if rec := doAs(t, a, secret, method, path, nil); rec.Code == http.StatusForbidden {
			t.Fatalf("%s %s was refused, but an agent needs it to route to its peers", method, path)
		}
	}

	for _, path := range []string{
		"/v1/nodes/n-1/instances",
		"/v1/nodes/n-1/network",
		"/v1/nodes/n-1/volumes",
		"/v1/dns/records",
		"/v1/images",
	} {
		t.Run("allows "+path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code == http.StatusForbidden {
				t.Fatalf("%s was refused, but an agent reconciles with it", path)
			}
		})
	}

	for _, path := range []string{"/v1/instances", "/v1/volumes", "/v1/networks", "/v1/tokens"} {
		t.Run("refuses "+path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d: a node has no business reading the whole platform",
					rec.Code, http.StatusForbidden)
			}
		})
	}
}

func TestANodeTokenCannotCreateWork(t *testing.T) {
	a := newTestApp(t)
	secret := nodeToken(t, a)

	body := `{"name":"sneaky","isolation":"container","image":"alpine:3.20"}`
	rec := doAs(t, a, secret, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: a compromised node must not be able to schedule work",
			rec.Code, http.StatusForbidden)
	}
}

func TestTheLastAdminTokenCannotBeRevoked(t *testing.T) {
	a := newTestApp(t)

	var list struct {
		Tokens []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(do(t, a, http.MethodGet, "/v1/tokens", nil).Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Tokens) != 1 {
		t.Fatalf("tokens = %d, want the bootstrap token", len(list.Tokens))
	}

	rec := do(t, a, http.MethodDelete, "/v1/tokens/"+list.Tokens[0].ID, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: revoking the only admin token locks everyone out",
			rec.Code, http.StatusConflict)
	}
}

func TestSecretsAreNotListed(t *testing.T) {
	a := newTestApp(t)
	nodeToken(t, a)

	body := do(t, a, http.MethodGet, "/v1/tokens", nil).Body.String()
	if strings.Contains(body, "mst_") {
		t.Fatalf("a token secret appeared in the listing: %s", body)
	}
}
