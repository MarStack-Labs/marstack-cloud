package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type registryBody struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	KeyID     string `json:"key_id"`
	CreatedAt string `json:"created_at"`
}

func addRegistry(t *testing.T, a *testApp, secret, body string) (int, registryBody, string) {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodPost, "/v1/registries", strings.NewReader(body))

	var created registryBody
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	return rec.Code, created, rec.Body.String()
}

func nodeRegistries(t *testing.T, a *testApp, nodeID string) []registryBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/registries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node registries: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Credentials []registryBody `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Credentials
}

func TestANodeGetsTheRegistryPasswordAndAnOperatorNever(t *testing.T) {
	a, nodeID := newSealingApp(t)

	code, created, _ := addRegistry(t, a, a.secret,
		`{"host":"ghcr.io","username":"marstack","password":"a-real-token"}`)
	if code != http.StatusCreated {
		t.Fatalf("add: %d", code)
	}
	if created.Host != "ghcr.io" || created.Username != "marstack" {
		t.Fatalf("created = %+v", created)
	}

	rec := doAs(t, a, a.secret, http.MethodGet, "/v1/registries", nil)
	if strings.Contains(rec.Body.String(), "a-real-token") {
		t.Fatalf("the operator list carries the password: %s", rec.Body.String())
	}

	var shape struct {
		Credentials []map[string]any `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &shape); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(shape.Credentials) != 1 {
		t.Fatalf("list = %+v, want the one credential", shape.Credentials)
	}

	wanted := map[string]bool{"id": true, "host": true, "username": true,
		"key_id": true, "created_at": true}
	for key := range shape.Credentials[0] {
		if !wanted[key] {
			t.Fatalf("the operator response carries %q. Only the node route may carry a "+
				"secret, and a field added here reaches every admin of every project", key)
		}
	}
	if !strings.Contains(rec.Body.String(), created.KeyID) {
		t.Fatalf("the list does not say which key sealed it: %s", rec.Body.String())
	}

	held := nodeRegistries(t, a, nodeID)
	if len(held) != 1 {
		t.Fatalf("the node was given %d credentials, want one", len(held))
	}
	if held[0].Password != "a-real-token" {
		t.Fatalf("password = %q, want it unsealed for the node: a credential the node "+
			"cannot read is a private image nothing can ever pull", held[0].Password)
	}
}

func TestARegistryPasswordIsRefusedWithNoSealingKey(t *testing.T) {
	a, _ := newBalancingApp(t)

	code, _, body := addRegistry(t, a, a.secret,
		`{"host":"ghcr.io","username":"marstack","password":"secret"}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: storing it in the clear and warning about it is not "+
			"protection", code, http.StatusConflict)
	}
	if !strings.Contains(body, "backup-key-file") {
		t.Fatalf("refusal = %s, want it to name the flag that fixes it", body)
	}
}

func TestOneHostHoldsOneLogin(t *testing.T) {
	a, _ := newSealingApp(t)

	addRegistry(t, a, a.secret, `{"host":"ghcr.io","username":"one","password":"x"}`)

	code, _, body := addRegistry(t, a, a.secret,
		`{"host":"GHCR.IO","username":"two","password":"y"}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: one pull cannot be made with two logins, and the "+
			"host is not case sensitive", code, http.StatusConflict)
	}
	if !strings.Contains(body, "ghcr.io") {
		t.Fatalf("refusal = %s, want it to name the host", body)
	}
}

func TestAHostIsAHostNotAUrl(t *testing.T) {
	a, _ := newSealingApp(t)

	for _, host := range []string{
		"https://ghcr.io", "ghcr.io/marstack", "ghcr.io/", "not a host", "",
	} {
		body, _ := json.Marshal(map[string]string{
			"host": host, "username": "u", "password": "p",
		})
		code, _, refused := addRegistry(t, a, a.secret, string(body))
		if code != http.StatusBadRequest {
			t.Errorf("host %q: status = %d, want %d", host, code, http.StatusBadRequest)
			continue
		}
		if strings.Contains(host, "/") && !strings.Contains(refused, "without a scheme") {
			t.Errorf("host %q was refused with %s, want it to say what a host looks like: "+
				"the pattern refuses this anyway, so the guard earns its place only by "+
				"explaining what to type instead", host, refused)
		}
	}
}

func TestARemovedLoginStopsReachingNodes(t *testing.T) {
	a, nodeID := newSealingApp(t)

	_, created, _ := addRegistry(t, a, a.secret,
		`{"host":"ghcr.io","username":"marstack","password":"token"}`)

	if rec := doAs(t, a, a.secret, http.MethodDelete,
		"/v1/registries/"+created.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if held := nodeRegistries(t, a, nodeID); len(held) != 0 {
		t.Fatalf("the node still gets %d credentials", len(held))
	}
}

func TestALoginCanBeRemovedByHost(t *testing.T) {
	a, _ := newSealingApp(t)
	addRegistry(t, a, a.secret, `{"host":"ghcr.io","username":"u","password":"p"}`)

	if rec := doAs(t, a, a.secret, http.MethodDelete,
		"/v1/registries/ghcr.io", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete by host: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAMemberCannotSeeOrChangeRegistryLogins(t *testing.T) {
	a, _ := newSealingApp(t)
	addRegistry(t, a, a.secret, `{"host":"ghcr.io","username":"u","password":"p"}`)

	member := memberToken(t, a)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/registries", ""},
		{http.MethodPost, "/v1/registries", `{"host":"x.io","username":"u","password":"p"}`},
		{http.MethodDelete, "/v1/registries/ghcr.io", ""},
	} {
		rec := doAs(t, a, member, call.method, call.path, strings.NewReader(call.body))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want %d: a registry login is infrastructure, and "+
				"a tenant reading it reads every other tenant's pull credential too",
				call.method, call.path, rec.Code, http.StatusForbidden)
		}
	}
}
