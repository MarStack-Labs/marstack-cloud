package firewall

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newTestModule(t *testing.T) (http.Handler, *Module) {
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

	mux := http.NewServeMux()
	m.Routes(mux)
	return mux, m
}

const testProject = "prj-test"

func request(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(scope.With(req.Context(), scope.Scope{ProjectID: testProject}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func create(t *testing.T, h http.Handler, body string) response {
	t.Helper()

	rec := request(t, h, http.MethodPost, "/v1/firewalls", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func TestRulesAreNormalised(t *testing.T) {
	h, _ := newTestModule(t)

	created := create(t, h, `{"name":"web","rules":[
		{"protocol":"tcp","from_port":80},
		{"from_port":443,"source":"10.20.0.99/16"},
		{"protocol":"icmp","from_port":8}
	]}`)

	if len(created.Rules) != 3 {
		t.Fatalf("rules = %+v", created.Rules)
	}
	if created.Rules[0].ToPort != 80 {
		t.Fatalf("to_port = %d, want a single port rule to close its own range", created.Rules[0].ToPort)
	}
	if created.Rules[1].Protocol != ProtocolTCP {
		t.Fatalf("protocol = %q, want tcp by default", created.Rules[1].Protocol)
	}
	if created.Rules[1].Source != "10.20.0.0/16" {
		t.Fatalf("source = %q, want the prefix masked so two spellings cannot differ",
			created.Rules[1].Source)
	}
	if created.Rules[2].FromPort != 0 || created.Rules[2].ToPort != 0 {
		t.Fatalf("icmp rule = %+v, want no ports: icmp has none", created.Rules[2])
	}
}

func TestBadRulesAreRejected(t *testing.T) {
	h, _ := newTestModule(t)

	cases := map[string]string{
		"no name":            `{"rules":[]}`,
		"unknown protocol":   `{"name":"a","rules":[{"protocol":"sctp","from_port":80}]}`,
		"tcp without a port": `{"name":"a","rules":[{"protocol":"tcp"}]}`,
		"port out of range":  `{"name":"a","rules":[{"protocol":"tcp","from_port":70000}]}`,
		"inverted range":     `{"name":"a","rules":[{"protocol":"tcp","from_port":100,"to_port":50}]}`,
		"source not a cidr":  `{"name":"a","rules":[{"protocol":"tcp","from_port":80,"source":"10.20.0.1"}]}`,
		"v4 written as v6": `{"name":"a","rules":` +
			`[{"protocol":"tcp","from_port":80,"source":"::ffff:10.20.0.0/112"}]}`,
		"unknown field": `{"name":"a","rules":[{"protocol":"tcp","port":80}]}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := request(t, h, http.MethodPost, "/v1/firewalls", body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestAFirewallWithNoRulesLetsNothingIn(t *testing.T) {
	h, _ := newTestModule(t)

	created := create(t, h, `{"name":"locked"}`)
	if len(created.Rules) != 0 {
		t.Fatalf("rules = %+v, want none: naming a firewall with no rules is how you close a port",
			created.Rules)
	}
}

func TestRulesCanBeReplaced(t *testing.T) {
	h, _ := newTestModule(t)
	create(t, h, `{"name":"web","rules":[{"protocol":"tcp","from_port":80}]}`)

	rec := request(t, h, http.MethodPut, "/v1/firewalls/web/rules",
		`{"rules":[{"protocol":"tcp","from_port":8000,"to_port":8100,"source":"10.20.0.0/16"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}

	var after response
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(after.Rules) != 1 || after.Rules[0].FromPort != 8000 || after.Rules[0].ToPort != 8100 {
		t.Fatalf("rules = %+v, want the set replaced, not merged", after.Rules)
	}
}

func TestExistsAnswersForInstanceValidation(t *testing.T) {
	h, m := newTestModule(t)
	created := create(t, h, `{"name":"web"}`)

	known, err := m.ExistsIn(context.Background(), created.ID, testProject)
	if err != nil || !known {
		t.Fatalf("exists = %v, err = %v", known, err)
	}

	known, err = m.ExistsIn(context.Background(), "fw-nope", testProject)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if known {
		t.Fatal("an unknown firewall reported as existing, which would silently mean no rules")
	}
}

func TestDuplicateNameIsRejected(t *testing.T) {
	h, _ := newTestModule(t)
	create(t, h, `{"name":"web"}`)

	if rec := request(t, h, http.MethodPost, "/v1/firewalls", `{"name":"web"}`); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestDeletingAFirewall(t *testing.T) {
	h, _ := newTestModule(t)
	create(t, h, `{"name":"web"}`)

	if rec := request(t, h, http.MethodDelete, "/v1/firewalls/web", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec := request(t, h, http.MethodGet, "/v1/firewalls/web", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAnIPv6SourceIsAccepted(t *testing.T) {
	h, _ := newTestModule(t)

	body := `{"name":"six","rules":[{"protocol":"tcp","from_port":443,"source":"fd00::/8"}]}`
	rec := request(t, h, http.MethodPost, "/v1/firewalls", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fd00::/8") {
		t.Fatalf("body = %s, want the source kept as given", rec.Body.String())
	}
}
