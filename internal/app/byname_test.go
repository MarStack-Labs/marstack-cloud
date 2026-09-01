package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func instanceNamed(t *testing.T, a *testApp, name string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"`+name+`","isolation":"container",`+
			`"image":"alpine:3.20"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func TestAnInstanceCanBeReadByName(t *testing.T) {
	a, _ := newBalancingApp(t)
	id := instanceNamed(t, a, "web-1")

	rec := do(t, a, http.MethodGet, "/v1/instances/web-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: every other resource takes a name or an id, and an "+
			"instance is the one people type most", rec.Code, http.StatusOK)
	}

	var found struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &found)
	if found.ID != id {
		t.Fatalf("id = %q, want %q", found.ID, id)
	}
}

func TestStoppingAndStartingByNameActuallyMoves(t *testing.T) {
	a, _ := newBalancingApp(t)
	instanceNamed(t, a, "web-1")

	for _, action := range []string{"stop", "start"} {
		rec := do(t, a, http.MethodPost, "/v1/instances/web-1/"+action, nil)
		if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
			t.Fatalf("%s: %d %s", action, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, a, http.MethodGet, "/v1/instances/web-1", nil)
	var held struct {
		Desired string `json:"desired_state"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &held)

	if held.Desired != "running" {
		t.Fatalf("desired = %q, want running: a name that resolves for the lookup and then "+
			"goes to the database unresolved reports success and changes nothing",
			held.Desired)
	}
}

func TestResizingByNameChangesTheInstance(t *testing.T) {
	a, _ := newBalancingApp(t)
	instanceNamed(t, a, "web-1")

	rec := do(t, a, http.MethodPost, "/v1/instances/web-1/resize",
		strings.NewReader(`{"vcpu":2,"memory_mib":1024}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("resize: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, a, http.MethodGet, "/v1/instances/web-1", nil)
	var held struct {
		VCPU      int `json:"vcpu"`
		MemoryMiB int `json:"memory_mib"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &held)

	if held.VCPU != 2 || held.MemoryMiB != 1024 {
		t.Fatalf("size = %dcpu/%dMi, want 2cpu/1024Mi", held.VCPU, held.MemoryMiB)
	}
}

func TestDeletingByNameReleasesWhatTheInstanceHeld(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := instanceNamed(t, a, "web-1")
	waitForNICs(t, a, nodeID, id, 1)

	if code, _ := publish(t, a, id, "", 8300); code != http.StatusCreated {
		t.Fatalf("publish: %d", code)
	}

	if rec := do(t, a, http.MethodDelete, "/v1/instances/web-1", nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if addresses := addressesOf(t, a, nodeID, id); len(addresses) != 0 {
		t.Fatalf("the address %v was never released. Deleting resolves the name for the "+
			"lookup and then releases addresses, volumes, forwards and balancers by "+
			"whatever was typed: a name releases nothing and leaves every one of them "+
			"pointing at an instance that is gone", addresses)
	}

	rec := do(t, a, http.MethodGet, "/v1/forwards", nil)
	if strings.Contains(rec.Body.String(), id) {
		t.Fatalf("a published port still points at the deleted instance: %s",
			rec.Body.String())
	}
}

func TestALogCanBeReadByName(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")
	shipLines(t, a, a.secret, nodeID, id, []string{"listening"})

	code, body := readLogs(t, a, "web-1", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d", code, http.StatusOK)
	}
	if len(body.Lines) != 1 || body.Lines[0].Text != "listening" {
		t.Fatalf("lines = %+v, want the one that was shipped", body.Lines)
	}
	if body.InstanceID != id {
		t.Fatalf("instance_id = %q, want the id it resolved to rather than the name that "+
			"was asked for", body.InstanceID)
	}
}

func TestAnIdWinsOverANameThatLooksLikeOne(t *testing.T) {
	a, _ := newBalancingApp(t)
	real := instanceNamed(t, a, "web-1")

	rec := do(t, a, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"`+real+`","isolation":"container",`+
			`"image":"alpine:3.20"}`))
	if rec.Code != http.StatusCreated {
		t.Skipf("an instance cannot be named after an id here: %d", rec.Code)
	}

	read := do(t, a, http.MethodGet, "/v1/instances/"+real, nil)
	var found struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(read.Body.Bytes(), &found)

	if found.ID != real {
		t.Fatalf("looking up %q found the instance named after it rather than the one it "+
			"identifies. An id has to win, or naming something after somebody else's id "+
			"shadows it", real)
	}
}

func TestANameFromAnotherProjectIsNotFound(t *testing.T) {
	a, _ := newBalancingApp(t)
	instanceNamed(t, a, "web-1")

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodGet, "/v1/instances/web-1", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: names are unique per project, so one project's "+
			"name must not resolve in another", rec.Code, http.StatusNotFound)
	}
}
