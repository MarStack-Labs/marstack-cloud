package instance

import (
	"encoding/json"
	"net/http"
	"testing"
)

func placedInstance(t *testing.T, h http.Handler, m *Module, name, nodeID string) response {
	t.Helper()

	created := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances",
		`{"name":"`+name+`","isolation":"container","image":"alpine:3.20","command":["/bin/sh","-c","sleep 100"]}`))

	if err := m.Assign(t.Context(), created.ID, nodeID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	return created
}

func decodeList(t *testing.T, h http.Handler, path string) listResponse {
	t.Helper()

	rec := request(t, h, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var list listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return list
}

func TestListForNodeReturnsOnlyThatNodesInstances(t *testing.T) {
	h, m := newTestModuleWithAssign(t)

	mine := placedInstance(t, h, m, "api-1", "n-mine")
	placedInstance(t, h, m, "api-2", "n-other")

	list := decodeList(t, h, "/v1/nodes/n-mine/instances")
	if len(list.Instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(list.Instances))
	}
	if list.Instances[0].ID != mine.ID {
		t.Fatalf("id = %q, want %q", list.Instances[0].ID, mine.ID)
	}
}

func TestListForNodeIncludesTheSpecTheAgentNeeds(t *testing.T) {
	h, m := newTestModuleWithAssign(t)
	placedInstance(t, h, m, "api-1", "n-mine")

	got := decodeList(t, h, "/v1/nodes/n-mine/instances").Instances[0]

	if got.Isolation == "" || got.Image == "" || got.VCPU == 0 || got.MemoryMiB == 0 {
		t.Fatalf("instance = %+v, want the runtime spec filled in", got)
	}
	if got.Desired == "" {
		t.Fatal("desired_state is empty; the agent has nothing to reconcile towards")
	}
}

func TestListForUnknownNodeIsEmptyNotAnError(t *testing.T) {
	h, _ := newTestModuleWithAssign(t)

	list := decodeList(t, h, "/v1/nodes/n-nobody/instances")
	if len(list.Instances) != 0 {
		t.Fatalf("instances = %d, want 0", len(list.Instances))
	}
}

func TestReportStatusRecordsObservedState(t *testing.T) {
	h, m := newTestModuleWithAssign(t)
	created := placedInstance(t, h, m, "api-1", "n-mine")

	rec := request(t, h, http.MethodPut,
		"/v1/nodes/n-mine/instances/"+created.ID+"/status",
		`{"observed_state":"running"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	got := decodeInstance(t, rec)
	if got.Observed != string(ObservedRunning) {
		t.Fatalf("observed_state = %q, want %q", got.Observed, ObservedRunning)
	}
	if got.Desired != string(DesiredRunning) {
		t.Fatalf("desired_state = %q, want the report to leave intent alone", got.Desired)
	}
}

func TestReportStatusCarriesAFailureMessage(t *testing.T) {
	h, m := newTestModuleWithAssign(t)
	created := placedInstance(t, h, m, "api-1", "n-mine")

	rec := request(t, h, http.MethodPut,
		"/v1/nodes/n-mine/instances/"+created.ID+"/status",
		`{"observed_state":"failed","message":"image alpine:3.20 is not present on this node"}`)

	got := decodeInstance(t, rec)
	if got.Observed != string(ObservedFailed) {
		t.Fatalf("observed_state = %q, want %q", got.Observed, ObservedFailed)
	}
	if got.ObservedMessage == "" {
		t.Fatal("observed_message is empty; a failure with no reason is not debuggable")
	}
}

func TestNodeCannotReportForAnotherNodesInstance(t *testing.T) {
	h, m := newTestModuleWithAssign(t)
	created := placedInstance(t, h, m, "api-1", "n-mine")

	rec := request(t, h, http.MethodPut,
		"/v1/nodes/n-other/instances/"+created.ID+"/status",
		`{"observed_state":"failed","message":"not mine to report"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec); code != "instance_not_on_node" {
		t.Fatalf("error code = %q, want %q", code, "instance_not_on_node")
	}

	untouched := decodeInstance(t, request(t, h, http.MethodGet, "/v1/instances/"+created.ID, ""))
	if untouched.Observed != string(ObservedPending) {
		t.Fatalf("observed_state = %q, want it untouched at %q", untouched.Observed, ObservedPending)
	}
}

func TestReportStatusRejectsUnknownState(t *testing.T) {
	h, m := newTestModuleWithAssign(t)
	created := placedInstance(t, h, m, "api-1", "n-mine")

	rec := request(t, h, http.MethodPut,
		"/v1/nodes/n-mine/instances/"+created.ID+"/status",
		`{"observed_state":"probably-fine"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if code := errorCode(t, rec); code != "invalid_observed_state" {
		t.Fatalf("error code = %q, want %q", code, "invalid_observed_state")
	}
}

func TestReportStatusRejectsOversizedMessage(t *testing.T) {
	h, m := newTestModuleWithAssign(t)
	created := placedInstance(t, h, m, "api-1", "n-mine")

	long := make([]byte, MaxObservedMessage+1)
	for i := range long {
		long[i] = 'x'
	}

	rec := request(t, h, http.MethodPut,
		"/v1/nodes/n-mine/instances/"+created.ID+"/status",
		`{"observed_state":"failed","message":"`+string(long)+`"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestPendingPlacementSkipsPlacedAndStoppedInstances(t *testing.T) {
	h, m := newTestModuleWithAssign(t)

	waiting := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances",
		`{"name":"waiting","isolation":"container","image":"alpine","command":["/bin/sh","-c","sleep 100"]}`))
	placedInstance(t, h, m, "placed", "n-mine")

	stopped := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances",
		`{"name":"stopped","isolation":"container","image":"alpine","command":["/bin/sh","-c","sleep 100"]}`))
	request(t, h, http.MethodPost, "/v1/instances/"+stopped.ID+"/stop", "")

	pending, err := m.PendingPlacement(t.Context())
	if err != nil {
		t.Fatalf("pending placement: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != waiting.ID {
		t.Fatalf("pending = %+v, want only %s", pending, waiting.ID)
	}
}
