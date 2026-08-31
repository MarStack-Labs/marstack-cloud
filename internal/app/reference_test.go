package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func createInstanceOn(t *testing.T, a *testApp, request string) (int, string) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(request))

	var body struct {
		ID    string `json:"id"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code == http.StatusCreated {
		return rec.Code, body.ID
	}
	return rec.Code, body.Error.Code
}

func TestAnInstanceCannotNameANetworkThatDoesNotExist(t *testing.T) {
	a, _ := newBalancingApp(t)

	code, reason := createInstanceOn(t, a,
		`{"name":"lost","isolation":"container","image":"alpine:3.20",`+
			`"network_id":"net-nothing"}`)

	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: the instance was accepted and would sit there "+
			"never getting an address", code, http.StatusBadRequest)
	}
	if reason != "unknown_network" {
		t.Fatalf("code = %q, want unknown_network", reason)
	}
}

func TestAnExtraNetworkIsCheckedToo(t *testing.T) {
	a, _ := newBalancingApp(t)
	first := newNetwork(t, a, "front", "10.130.0.0/16")

	code, reason := createInstanceOn(t, a,
		`{"name":"half","isolation":"container","image":"alpine:3.20",`+
			`"network_id":"`+first+`","extra_networks":["net-nothing"]}`)

	if code != http.StatusBadRequest || reason != "unknown_network" {
		t.Fatalf("status = %d code = %q, want a refusal: an unchecked second interface is "+
			"the same gap as an unchecked first one", code, reason)
	}
}

func TestANetworkInAnotherProjectIsNotUsable(t *testing.T) {
	a, _ := newBalancingApp(t)
	mine := newNetwork(t, a, "mine", "10.131.0.0/16")

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, other, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"borrowed","isolation":"container","image":"alpine:3.20",`+
			`"network_id":"`+mine+`"}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: another project's network must answer exactly the "+
			"way one that does not exist answers, or the refusal confirms it exists",
			rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "unknown_network") {
		t.Fatalf("body = %s, want the same code an absent network gets", rec.Body.String())
	}
}

func TestTheSameExtraNetworkTwiceIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)
	first := newNetwork(t, a, "front", "10.132.0.0/16")
	second := newNetwork(t, a, "back", "10.133.0.0/16")

	code, reason := createInstanceOn(t, a,
		`{"name":"double","isolation":"container","image":"alpine:3.20",`+
			`"network_id":"`+first+`","extra_networks":["`+second+`","`+second+`"]}`)

	if code != http.StatusBadRequest || reason != "invalid_networks" {
		t.Fatalf("status = %d code = %q, want a refusal: the guard covered a repeat of the "+
			"first network but not a repeat among the extras", code, reason)
	}
}

func TestAGoodNetworkStillWorks(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	first := newNetwork(t, a, "front", "10.134.0.0/16")
	second := newNetwork(t, a, "back", "10.135.0.0/16")

	code, id := createInstanceOn(t, a,
		`{"name":"fine","isolation":"container","image":"alpine:3.20",`+
			`"network_id":"`+first+`","extra_networks":["`+second+`"]}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want the instance created: the check must not refuse what "+
			"does exist", code)
	}
	waitForNICs(t, a, nodeID, id, 2)
}

func TestAServiceTemplateNamingNothingIsRefusedAtOnce(t *testing.T) {
	a, _ := newBalancingApp(t)

	rec := do(t, a, http.MethodPost, "/v1/services",
		strings.NewReader(`{"name":"web","replicas":1,"isolation":"container",`+
			`"image":"alpine:3.20","firewall_id":"fw-nothing"}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: the loop would have found out instead, one blocked "+
			"pass at a time", rec.Code, http.StatusBadRequest)
	}
}

func TestABadRevisionIsRefusedBeforeItCostsAReplica(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":3,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21","firewall_id":"fw-nothing"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", code, http.StatusBadRequest)
	}

	settle(t, a, 3)
	held := revisionOf(t, a, created.ID)

	if held.Revision != 1 {
		t.Fatalf("revision = %d, want 1: a refused publish must not have moved the counter",
			held.Revision)
	}
	if len(held.Members) != 3 {
		t.Fatalf("members = %d, want 3: nothing was retired, because there was never a "+
			"revision to roll onto", len(held.Members))
	}
	if held.Blocked != "" {
		t.Fatalf("blocked = %q, want nothing: the service is healthy and the caller was "+
			"told at the door", held.Blocked)
	}
}

func TestAnUnknownNetworkInATemplateIsRefusedToo(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	if code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21","network_id":"net-nothing"}`,
	); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", code, http.StatusBadRequest)
	}
}
