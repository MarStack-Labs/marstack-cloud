package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type nodeListBody struct {
	Nodes []nodeBody `json:"nodes"`
}

func setLabels(
	t *testing.T, a *testApp, secret, nodeID, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return doAs(t, a, secret, http.MethodPut, "/v1/nodes/"+nodeID+"/labels",
		strings.NewReader(body))
}

func labelsOf(t *testing.T, a *testApp, nodeID string) map[string]string {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get node: %d %s", rec.Code, rec.Body.String())
	}

	var n nodeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return n.Labels
}

func TestLabelsAreSetAndReadBack(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	rec := setLabels(t, a, a.secret, nodeID, `{"labels":{"disk":"nvme","tier":"prod"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set labels: %d %s", rec.Code, rec.Body.String())
	}

	got := labelsOf(t, a, nodeID)
	if got["disk"] != "nvme" || got["tier"] != "prod" {
		t.Fatalf("labels = %v, want both written back", got)
	}
}

func TestSettingLabelsReplacesTheWholeSet(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	setLabels(t, a, a.secret, nodeID, `{"labels":{"disk":"nvme","tier":"prod"}}`)
	setLabels(t, a, a.secret, nodeID, `{"labels":{"disk":"spinning"}}`)

	got := labelsOf(t, a, nodeID)
	if got["disk"] != "spinning" {
		t.Fatalf("disk = %q, want the new value", got["disk"])
	}
	if _, still := got["tier"]; still {
		t.Fatalf("labels = %v, want tier gone: a put replaces rather than merges", got)
	}
}

func TestLabelsSurviveTheAgentRegisteringAgain(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	setLabels(t, a, a.secret, nodeID, `{"labels":{"tier":"prod"}}`)

	registerNode(t, a, "bm-1", "rack-a")

	if got := labelsOf(t, a, nodeID); got["tier"] != "prod" {
		t.Fatalf("labels = %v, want an operator's decision to outlive an agent restart", got)
	}
}

func TestANodeTokenCannotLabelANode(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	rec := setLabels(t, a, nodeToken(t, a), nodeID, `{"labels":{"tier":"prod"}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: a node that could label itself could pull work to itself",
			rec.Code, http.StatusForbidden)
	}
}

func TestListingNodesFiltersOnEveryLabelGiven(t *testing.T) {
	a, first := newBalancingApp(t)
	second := registerNode(t, a, "bm-2", "rack-b")

	setLabels(t, a, a.secret, first, `{"labels":{"disk":"nvme","tier":"prod"}}`)
	setLabels(t, a, a.secret, second, `{"labels":{"disk":"nvme","tier":"dev"}}`)

	both := listNodes(t, a, "?label=disk=nvme")
	if len(both) != 2 {
		t.Fatalf("nodes = %d, want both carrying disk=nvme", len(both))
	}

	one := listNodes(t, a, "?label=disk=nvme&label=tier=prod")
	if len(one) != 1 || one[0].ID != first {
		t.Fatalf("nodes = %v, want only the node matching every label", one)
	}

	none := listNodes(t, a, "?label=disk=nvme&label=tier=staging")
	if len(none) != 0 {
		t.Fatalf("nodes = %d, want none: no node carries that pair", len(none))
	}
}

func listNodes(t *testing.T, a *testApp, query string) []nodeBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list nodes%s: %d %s", query, rec.Code, rec.Body.String())
	}

	var body nodeListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Nodes
}

func TestABadLabelIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	for _, body := range []string{
		`{"labels":{"":"prod"}}`,
		`{"labels":{"tier":""}}`,
		`{"labels":{"Tier":"prod"}}`,
		`{"labels":{"tier":"prod!"}}`,
	} {
		rec := setLabels(t, a, a.secret, nodeID, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
	}

	rec := do(t, a, http.MethodGet, "/v1/nodes?label=justakey", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("filter without a value: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
