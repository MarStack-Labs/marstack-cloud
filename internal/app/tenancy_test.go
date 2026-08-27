package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/platform/project"
)

func newProject(t *testing.T, a *testApp, name string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/projects",
		strings.NewReader(`{"name":"`+name+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func tokenIn(t *testing.T, a *testApp, name, projectID string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"`+name+`","role":"member","project_id":"`+projectID+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.Secret
}

func newInstance(t *testing.T, a *testApp, secret, name string) string {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"container","image":"alpine:3.20"}`
	rec := doAs(t, a, secret, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create instance: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func instanceNames(t *testing.T, a *testApp, secret string) []string {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodGet, "/v1/instances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Instances []struct {
			Name string `json:"name"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	names := make([]string, 0, len(body.Instances))
	for _, in := range body.Instances {
		names = append(names, in.Name)
	}
	return names
}

func TestAProjectSeesOnlyItsOwnInstances(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	newInstance(t, a, a.secret, "ours")
	newInstance(t, a, theirs, "theirs")

	mine := instanceNames(t, a, a.secret)
	if len(mine) != 1 || mine[0] != "ours" {
		t.Fatalf("the default project sees %v, want only its own", mine)
	}

	yours := instanceNames(t, a, theirs)
	if len(yours) != 1 || yours[0] != "theirs" {
		t.Fatalf("tenant-b sees %v, want only its own", yours)
	}
}

func TestAnInstanceInAnotherProjectIsNotFoundRatherThanForbidden(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)
	id := newInstance(t, a, a.secret, "private")

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/instances/" + id},
		{http.MethodDelete, "/v1/instances/" + id},
		{http.MethodPost, "/v1/instances/" + id + "/start"},
		{http.MethodPost, "/v1/instances/" + id + "/stop"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := doAs(t, a, theirs, c.method, c.path, nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d: a 403 would confirm the id exists",
					rec.Code, http.StatusNotFound)
			}
		})
	}
}

func TestTwoProjectsMayEachHaveAnInstanceOfTheSameName(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	newInstance(t, a, a.secret, "web")
	newInstance(t, a, theirs, "web")
}

func TestEachProjectGetsItsOwnNetworkOnADistinctRange(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	newInstance(t, a, a.secret, "ours")
	newInstance(t, a, theirs, "theirs")

	ranges := map[string]string{}
	for name, secret := range map[string]string{"default": a.secret, "tenant-b": theirs} {
		rec := doAs(t, a, secret, http.MethodGet, "/v1/networks", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s list networks: %d %s", name, rec.Code, rec.Body.String())
		}

		var body struct {
			Networks []struct {
				CIDR string `json:"cidr"`
			} `json:"networks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Networks) != 1 {
			t.Fatalf("%s sees %d networks, want exactly its own", name, len(body.Networks))
		}
		ranges[name] = body.Networks[0].CIDR
	}

	if ranges["default"] == ranges["tenant-b"] {
		t.Fatalf("both projects landed on %s, and routing carries no encapsulation, so two "+
			"projects on one range would collide on the wire", ranges["default"])
	}
}

func TestAProjectHoldingAnInstanceCannotBeDeleted(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)
	newInstance(t, a, theirs, "busy")

	rec := do(t, a, http.MethodDelete, "/v1/projects/"+other, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestTheDefaultProjectExistsAtBoot(t *testing.T) {
	a := newTestApp(t)

	rec := do(t, a, http.MethodGet, "/v1/projects/"+project.DefaultID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: every migration backfills that id", rec.Code, http.StatusOK)
	}
}
