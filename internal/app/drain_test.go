package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

type nodeBody struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Status      string            `json:"status"`
	Schedulable bool              `json:"schedulable"`
	Draining    bool              `json:"draining"`
	Labels      map[string]string `json:"labels"`
}

func nodeAct(t *testing.T, a *testApp, nodeID, verb string) nodeBody {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/nodes/"+nodeID+"/"+verb, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", verb, rec.Code, rec.Body.String())
	}

	var body nodeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func readNode(t *testing.T, a *testApp, nodeID string) nodeBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read node: %d %s", rec.Code, rec.Body.String())
	}

	var body nodeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestANodeStartsSchedulable(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	n := readNode(t, a, nodeID)
	if !n.Schedulable {
		t.Fatal("a freshly registered node is not schedulable")
	}
	if n.Draining {
		t.Fatal("a freshly registered node is draining")
	}
}

func TestCordoningStopsNewPlacementWithoutTouchingWhatRuns(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	running := newInstance(t, a, a.secret, "web-1")
	if placed := waitForPlacement(t, a, running); placed == "" {
		t.Fatal("never placed")
	}

	if n := nodeAct(t, a, nodeID, "cordon"); n.Schedulable {
		t.Fatal("the node is still schedulable after cordon")
	}

	waiting := newInstance(t, a, a.secret, "web-2")
	if placed := waitForPlacement(t, a, waiting); placed != "" {
		t.Fatalf("placed on %s, want it to wait while the only node is cordoned", placed)
	}

	if instanceNodeID(t, a, running) != nodeID {
		t.Fatal("cordoning moved something that was already running")
	}
}

func TestUncordoningLetsTheWaitingWorkloadLand(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	nodeAct(t, a, nodeID, "cordon")
	waiting := newInstance(t, a, a.secret, "web-1")
	if placed := waitForPlacement(t, a, waiting); placed != "" {
		t.Fatalf("placed on %s while cordoned", placed)
	}

	if n := nodeAct(t, a, nodeID, "uncordon"); !n.Schedulable {
		t.Fatal("the node is not schedulable after uncordon")
	}
	if placed := waitForPlacement(t, a, waiting); placed != nodeID {
		t.Fatalf("placed on %q, want %s once the node reopened", placed, nodeID)
	}
}

func TestDrainCordonsAndMarksDraining(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	n := nodeAct(t, a, nodeID, "drain")
	if n.Schedulable {
		t.Fatal("draining a node left it schedulable, so it could take work back")
	}
	if !n.Draining {
		t.Fatal("draining a node did not mark it draining")
	}
}

func TestUncordonCancelsADrain(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	nodeAct(t, a, nodeID, "drain")
	n := nodeAct(t, a, nodeID, "uncordon")

	if n.Draining {
		t.Fatal("uncordon left the drain running")
	}
	if !n.Schedulable {
		t.Fatal("uncordon did not reopen the node")
	}
}

func TestACordonSurvivesTheAgentRegisteringAgain(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	nodeAct(t, a, nodeID, "cordon")

	rec := post(t, a, "/v1/nodes/register",
		`{"name":"bm-1","zone":"rack-a","arch":"arm64","os":"linux","cpus":4,`+
			`"memory_mib":6144,"agent_version":"test"}`)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("re-register: %d %s", rec.Code, rec.Body.String())
	}

	if n := readNode(t, a, nodeID); n.Schedulable {
		t.Fatal("restarting an agent quietly uncordoned its node, which would send work " +
			"back to a machine somebody is working on")
	}
}

func TestCordonAndDrainAreAdministrative(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	member := tokenIn(t, a, "worker", "prj-default")
	for _, verb := range []string{"cordon", "uncordon", "drain"} {
		rec := doAs(t, a, member, http.MethodPost, "/v1/nodes/"+nodeID+"/"+verb, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as a member = %d, want %d", verb, rec.Code, http.StatusForbidden)
		}
	}
}

func TestDrainingAnUnknownNodeIsNotFound(t *testing.T) {
	a, _ := newBalancingApp(t)

	rec := do(t, a, http.MethodPost, "/v1/nodes/n-ghost/drain", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
