package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
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
		"/v1/nodes/n-1/images",
		"/v1/nodes/n-1/firewalls",
		"/v1/dns/records",
	} {
		t.Run("allows "+path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code == http.StatusForbidden {
				t.Fatalf("%s was refused, but an agent reconciles with it", path)
			}
		})
	}

	for _, path := range []string{
		"/v1/instances", "/v1/volumes", "/v1/networks", "/v1/tokens",
		"/v1/images", "/v1/firewalls",
	} {
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

func auditEntries(t *testing.T, a *testApp) []struct {
	Actor  string `json:"actor"`
	Role   string `json:"role"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
} {
	t.Helper()

	var body struct {
		Entries []struct {
			Actor  string `json:"actor"`
			Role   string `json:"role"`
			Method string `json:"method"`
			Path   string `json:"path"`
			Status int    `json:"status"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(do(t, a, http.MethodGet, "/v1/audit", nil).Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Entries
}

func TestTheAuditTrailNamesWhoChangedWhat(t *testing.T) {
	a := newTestApp(t)

	do(t, a, http.MethodPost, "/v1/networks", strings.NewReader(`{"name":"audited","cidr":"10.70.0.0/16"}`))

	entries := auditEntries(t, a)
	if len(entries) == 0 {
		t.Fatal("nothing was recorded")
	}

	found := false
	for _, entry := range entries {
		if entry.Method == "POST" && entry.Path == "/v1/networks" {
			found = true
			if entry.Actor != "bootstrap" || entry.Role != "admin" {
				t.Fatalf("entry = %+v, want the token that did it", entry)
			}
			if entry.Status != http.StatusCreated {
				t.Fatalf("status = %d", entry.Status)
			}
		}
	}
	if !found {
		t.Fatalf("the create is missing from %+v", entries)
	}
}

func TestReadsAreNotRecorded(t *testing.T) {
	a := newTestApp(t)

	for range 3 {
		do(t, a, http.MethodGet, "/v1/instances", nil)
	}

	for _, entry := range auditEntries(t, a) {
		if entry.Method == "GET" && entry.Path == "/v1/instances" {
			t.Fatal("a plain read was recorded: the trail would drown in polling")
		}
	}
}

func TestRefusedRequestsAreRecordedWithoutAnActor(t *testing.T) {
	a := newTestApp(t)

	doAs(t, a, "", http.MethodPost, "/v1/instances", strings.NewReader(`{}`))
	doAs(t, a, "mst_notarealtoken", http.MethodDelete, "/v1/networks/nw-1", nil)

	entries := auditEntries(t, a)

	denied := 0
	for _, entry := range entries {
		if entry.Status == http.StatusUnauthorized {
			denied++
			if entry.Actor != "" {
				t.Fatalf("entry = %+v, want no actor: the caller never proved who they were", entry)
			}
		}
	}
	if denied < 2 {
		t.Fatalf("recorded %d denials in %+v, want both attempts", denied, entries)
	}
}

func TestANodeTokenCannotReadTheTrail(t *testing.T) {
	a := newTestApp(t)
	secret := nodeToken(t, a)

	if rec := doAs(t, a, secret, http.MethodGet, "/v1/audit", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: a node has no business reading who did what",
			rec.Code, http.StatusForbidden)
	}
}

func TestTokenSecretsNeverReachTheTrail(t *testing.T) {
	a := newTestApp(t)
	secret := nodeToken(t, a)

	body := do(t, a, http.MethodGet, "/v1/audit", nil).Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "mst_") {
		t.Fatalf("a token secret appeared in the audit trail: %s", body)
	}
}

func TestTheReconcileLoopDoesNotFloodTheTrail(t *testing.T) {
	a := newTestApp(t)
	secret := nodeToken(t, a)

	for range 5 {
		doAs(t, a, secret, http.MethodPut, "/v1/nodes/n-1/usage",
			strings.NewReader(`{"cpu_percent":1,"memory_used_mib":10,"memory_mib":100}`))
		doAs(t, a, secret, http.MethodPut, "/v1/nodes/n-1/images", strings.NewReader(`{"images":[]}`))
	}

	for _, entry := range auditEntries(t, a) {
		if entry.Role == "node" && entry.Status < 400 {
			t.Fatalf("entry = %+v: routine reconcile traffic buries the decisions an operator "+
				"needs to see, and the access log already carries it", entry)
		}
	}
}

func TestARefusedNodeTokenIsNamed(t *testing.T) {
	a := newTestApp(t)
	secret := nodeToken(t, a)

	doAs(t, a, secret, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"sneaky","isolation":"container","image":"alpine:3.20"}`))

	for _, entry := range auditEntries(t, a) {
		if entry.Status != http.StatusForbidden {
			continue
		}
		if entry.Actor != "bm-1" || entry.Role != "node" {
			t.Fatalf("entry = %+v, want the token that was refused: knowing who tried is the "+
				"whole point of recording a denial", entry)
		}
		return
	}
	t.Fatal("the refusal was not recorded")
}

func memberToken(t *testing.T, a *testApp) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"dev-1","role":"member"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create a member token: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret    string `json:"secret"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ProjectID == "" {
		t.Fatal("the token names no project, so nothing could scope its work")
	}
	return created.Secret
}

func TestAMemberTokenWorksOnResourcesButNotOnGovernance(t *testing.T) {
	a := newTestApp(t)
	secret := memberToken(t, a)

	for _, path := range []string{"/v1/instances", "/v1/volumes", "/v1/networks", "/v1/firewalls"} {
		t.Run("allows "+path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}

	for _, path := range []string{"/v1/tokens", "/v1/audit", "/v1/projects", "/v1/nodes"} {
		t.Run("refuses "+path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d: a member administers nothing",
					rec.Code, http.StatusForbidden)
			}
		})
	}
}

func TestAMemberCannotMintTokens(t *testing.T) {
	a := newTestApp(t)
	secret := memberToken(t, a)

	rec := doAs(t, a, secret, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"escalated","role":"admin"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: minting an admin token is privilege escalation",
			rec.Code, http.StatusForbidden)
	}
}

func TestAMemberCannotCreateAProject(t *testing.T) {
	a := newTestApp(t)
	secret := memberToken(t, a)

	rec := doAs(t, a, secret, http.MethodPost, "/v1/projects",
		strings.NewReader(`{"name":"mine"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestTheBootstrapTokenLandsInTheDefaultProject(t *testing.T) {
	a := newTestApp(t)

	var list struct {
		Tokens []struct {
			Name      string `json:"name"`
			ProjectID string `json:"project_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(do(t, a, http.MethodGet, "/v1/tokens", nil).Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, entry := range list.Tokens {
		if entry.Name != token.BootstrapName {
			continue
		}
		if entry.ProjectID == "" {
			t.Fatal("the bootstrap token names no project, so it could not create scoped work")
		}
		return
	}
	t.Fatal("the bootstrap token is missing")
}

func TestAnExpiredTokenIsRefusedByTheAPI(t *testing.T) {
	a, clock := newTickingApp(t)

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"brief","role":"member","expires_in":"1h"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret    string `json:"secret"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ExpiresAt == "" {
		t.Fatal("the token reports no expiry, so nobody could tell it was temporary")
	}

	if rec := doAs(t, a, created.Secret, http.MethodGet, "/v1/instances", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the token to work while it is valid", rec.Code)
	}

	*clock = clock.Add(2 * time.Hour)

	rec = doAs(t, a, created.Secret, http.MethodGet, "/v1/instances", nil)
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
	if body.Error.Code != "token_expired" {
		t.Fatalf("code = %q, want an answer that names why", body.Error.Code)
	}
}

func TestARefusedExpiredTokenIsRecorded(t *testing.T) {
	a, clock := newTickingApp(t)

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"brief","role":"member","expires_in":"1h"}`))
	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	*clock = clock.Add(2 * time.Hour)
	doAs(t, a, created.Secret, http.MethodDelete, "/v1/networks/nw-1", nil)

	for _, entry := range auditEntries(t, a) {
		if entry.Status == http.StatusUnauthorized {
			return
		}
	}
	t.Fatal("an expired token was turned away without a trace, so nobody could explain the outage")
}

func viewerToken(t *testing.T, a *testApp) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"oncall","role":"viewer"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create a viewer token: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.Secret
}

func TestAViewerReadsItsProjectAndChangesNothing(t *testing.T) {
	a := newTestApp(t)
	secret := viewerToken(t, a)

	for _, path := range []string{
		"/v1/instances", "/v1/volumes", "/v1/networks", "/v1/firewalls",
		"/v1/backups", "/v1/snapshots", "/v1/images", "/v1/usage",
	} {
		t.Run("reads "+path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}

	mutations := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/instances", `{"name":"x","isolation":"container","image":"alpine:3.20"}`},
		{http.MethodPost, "/v1/volumes", `{"name":"x","size_gib":1}`},
		{http.MethodPost, "/v1/networks", `{"name":"x","cidr":"10.90.0.0/16"}`},
		{http.MethodPost, "/v1/firewalls", `{"name":"x","rules":[]}`},
		{http.MethodDelete, "/v1/instances/i-anything", ""},
		{http.MethodDelete, "/v1/volumes/vol-anything", ""},
		{http.MethodPost, "/v1/instances/i-anything/stop", ""},
	}

	for _, m := range mutations {
		t.Run("refuses "+m.method+" "+m.path, func(t *testing.T) {
			var body io.Reader
			if m.body != "" {
				body = strings.NewReader(m.body)
			}
			if rec := doAs(t, a, secret, m.method, m.path, body); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d: a viewer must not change anything",
					rec.Code, http.StatusForbidden)
			}
		})
	}
}

func TestEveryRouteAViewerReachesIsARead(t *testing.T) {
	for _, pattern := range readsOf(memberPaths) {
		if !strings.HasPrefix(pattern, "GET ") && !strings.HasPrefix(pattern, "HEAD ") {
			t.Fatalf("a viewer may call %q, which is not a read", pattern)
		}
	}
}

func TestAViewerCannotReachGovernance(t *testing.T) {
	a := newTestApp(t)
	secret := viewerToken(t, a)

	for _, path := range []string{"/v1/tokens", "/v1/audit", "/v1/projects", "/v1/nodes"} {
		t.Run(path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
			}
		})
	}
}
