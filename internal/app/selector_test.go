package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type placedBody struct {
	ID       string `json:"id"`
	NodeID   string `json:"node_id"`
	Observed string `json:"observed_state"`
	Message  string `json:"observed_message"`
}

func createSelected(t *testing.T, a *testApp, name, selector string) string {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"container","image":"alpine:3.20"`
	if selector != "" {
		body += `,"node_selector":` + selector
	}
	body += `}`

	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
	}

	var created placedBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func placement(t *testing.T, a *testApp, id string) placedBody {
	t.Helper()

	var last placedBody
	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		rec := do(t, a, http.MethodGet, "/v1/instances/"+id, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if last.NodeID != "" || last.Message != "" {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

func TestASelectorPlacesOnlyOnAMatchingNode(t *testing.T) {
	a, first := newBalancingApp(t)
	second := registerNode(t, a, "bm-2", "rack-b")

	setLabels(t, a, a.secret, first, `{"labels":{"disk":"nvme"}}`)
	setLabels(t, a, a.secret, second, `{"labels":{"disk":"spinning"}}`)

	for i := range 4 {
		id := createSelected(t, a, "fast-"+string(rune('a'+i)), `{"disk":"spinning"}`)
		got := placement(t, a, id)

		if got.NodeID != second {
			t.Fatalf("node = %q, want %s: the selector names the only node carrying disk=spinning",
				got.NodeID, second)
		}
	}
}

func TestAnUnmatchableSelectorHoldsTheInstanceAndSaysWhy(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	setLabels(t, a, a.secret, nodeID, `{"labels":{"disk":"nvme"}}`)

	id := createSelected(t, a, "picky", `{"disk":"tape"}`)
	got := placement(t, a, id)

	if got.NodeID != "" {
		t.Fatalf("node = %q, want nothing: no node carries disk=tape", got.NodeID)
	}
	if !strings.Contains(got.Message, "disk=tape") {
		t.Fatalf("message = %q, want it to name the selector nothing matched", got.Message)
	}
}

func TestAHeldInstanceIsPlacedOnceALabelArrives(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createSelected(t, a, "waiting", `{"tier":"prod"}`)
	if got := placement(t, a, id); got.NodeID != "" {
		t.Fatalf("node = %q, want nothing yet", got.NodeID)
	}

	setLabels(t, a, a.secret, nodeID, `{"labels":{"tier":"prod"}}`)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := placement(t, a, id); got.NodeID == nodeID {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("labelling the node never got the held instance placed, so a hold is permanent")
}

func TestNoSelectorStillPlacesAnywhere(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	setLabels(t, a, a.secret, nodeID, `{"labels":{"disk":"nvme"}}`)

	id := createSelected(t, a, "easy", "")
	if got := placement(t, a, id); got.NodeID != nodeID {
		t.Fatalf("node = %q, want %s: an instance asking for nothing takes any node",
			got.NodeID, nodeID)
	}
}

func TestABadSelectorIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	for _, selector := range []string{
		`{"":"prod"}`,
		`{"tier":""}`,
		`{"Tier":"prod"}`,
		`{"a":"1","b":"1","c":"1","d":"1","e":"1","f":"1","g":"1","h":"1","i":"1"}`,
	} {
		body := `{"name":"bad","isolation":"container","image":"alpine:3.20",` +
			`"node_selector":` + selector + `}`
		rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", selector, rec.Code, http.StatusBadRequest)
		}
	}
}
