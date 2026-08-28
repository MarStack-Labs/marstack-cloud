package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type serviceMemberBody struct {
	InstanceID string `json:"instance_id"`
	CreatedAt  string `json:"created_at"`
}

type serviceBody struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Replicas  int                 `json:"replicas"`
	Isolation string              `json:"isolation"`
	Image     string              `json:"image"`
	Group     string              `json:"placement_group"`
	Blocked   string              `json:"blocked"`
	Members   []serviceMemberBody `json:"members"`
}

func createService(t *testing.T, a *testApp, secret, body string) serviceBody {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodPost, "/v1/services", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create service: %d %s", rec.Code, rec.Body.String())
	}

	var created serviceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func readService(t *testing.T, a *testApp, secret, id string) serviceBody {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodGet, "/v1/services/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read service: %d %s", rec.Code, rec.Body.String())
	}

	var body serviceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func settle(t *testing.T, a *testApp, passes int) {
	t.Helper()

	for range passes {
		a.services.Reconcile(context.Background())
	}
}

func instanceCount(t *testing.T, a *testApp) int {
	t.Helper()
	return len(instanceNames(t, a, a.secret))
}

func TestAServiceMakesItsReplicas(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":3,"isolation":"container","image":"alpine:3.20"}`)
	if len(created.Members) != 0 {
		t.Fatal("a service should hold nothing before the loop has run")
	}

	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 3 {
		t.Fatalf("members = %d, want the three it was asked for", len(held.Members))
	}
	if instanceCount(t, a) != 3 {
		t.Fatalf("instances = %d, want three real workloads", instanceCount(t, a))
	}
}

func TestAServiceIsIdempotentOnceItHasEnough(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 5)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 2 {
		t.Fatalf("members = %d, want two after five passes rather than ten", len(held.Members))
	}
}

func TestADeletedReplicaIsReplaced(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	gone := held.Members[0].InstanceID

	rec := do(t, a, http.MethodDelete, "/v1/instances/"+gone, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete replica: %d %s", rec.Code, rec.Body.String())
	}

	settle(t, a, 1)

	after := readService(t, a, a.secret, created.ID)
	if len(after.Members) != 2 {
		t.Fatalf("members = %d, want the count held after one was deleted", len(after.Members))
	}
	for _, member := range after.Members {
		if member.InstanceID == gone {
			t.Fatal("the deleted replica is still counted as a member")
		}
	}

	entries := readEvents(t, a, a.secret, "?subject="+gone+"&kind=service.replica_lost")
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want the loss recorded", entries)
	}
}

func TestScalingUpAddsAndScalingDownRemoves(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	rec := do(t, a, http.MethodPost, "/v1/services/"+created.ID+"/scale",
		strings.NewReader(`{"replicas":3}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("scale up: %d %s", rec.Code, rec.Body.String())
	}
	settle(t, a, 1)

	if held := readService(t, a, a.secret, created.ID); len(held.Members) != 3 {
		t.Fatalf("members = %d, want three after scaling up", len(held.Members))
	}

	rec = do(t, a, http.MethodPost, "/v1/services/"+created.ID+"/scale",
		strings.NewReader(`{"replicas":1}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("scale down: %d %s", rec.Code, rec.Body.String())
	}
	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 1 {
		t.Fatalf("members = %d, want one after scaling down", len(held.Members))
	}
	if instanceCount(t, a) != 1 {
		t.Fatalf("instances = %d, want the removed replicas really gone", instanceCount(t, a))
	}
}

func TestScalingDownRemovesTheNewestReplicaFirst(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	oldest := readService(t, a, a.secret, created.ID).Members[0].InstanceID

	do(t, a, http.MethodPost, "/v1/services/"+created.ID+"/scale",
		strings.NewReader(`{"replicas":3}`))
	settle(t, a, 1)

	do(t, a, http.MethodPost, "/v1/services/"+created.ID+"/scale",
		strings.NewReader(`{"replicas":1}`))
	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 1 {
		t.Fatalf("members = %d, want one", len(held.Members))
	}
	if held.Members[0].InstanceID != oldest {
		t.Fatalf("kept %s, want the one that has been serving longest (%s)",
			held.Members[0].InstanceID, oldest)
	}
}

func TestScalingToZeroLeavesTheServiceAndNoReplicas(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	do(t, a, http.MethodPost, "/v1/services/"+created.ID+"/scale",
		strings.NewReader(`{"replicas":0}`))
	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 0 {
		t.Fatalf("members = %d, want none", len(held.Members))
	}
	if instanceCount(t, a) != 0 {
		t.Fatalf("instances = %d, want none", instanceCount(t, a))
	}
}

func TestDeletingAServiceRemovesItsReplicas(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	rec := do(t, a, http.MethodDelete, "/v1/services/"+created.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if instanceCount(t, a) != 0 {
		t.Fatalf("instances = %d, want the replicas gone with the service", instanceCount(t, a))
	}
	if rec := do(t, a, http.MethodGet, "/v1/services/"+created.ID,
		nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestAServiceRefusedByQuotaSaysWhyAndKeepsWhatItHas(t *testing.T) {
	a, _ := newBalancingApp(t)
	setQuota(t, a, "prj-default", `{"instances":2}`)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":4,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 2)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 2 {
		t.Fatalf("members = %d, want it to stop at the quota rather than fail entirely",
			len(held.Members))
	}
	if held.Blocked == "" {
		t.Fatal("a service that cannot reach its count does not say why")
	}

	entries := readEvents(t, a, a.secret, "?kind=service.blocked")
	if len(entries) != 1 {
		t.Fatalf("events = %d, want the block recorded once rather than once per pass",
			len(entries))
	}
}

func TestAServiceStopsSayingItIsStuckOnceItIsNot(t *testing.T) {
	a, _ := newBalancingApp(t)
	setQuota(t, a, "prj-default", `{"instances":1}`)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 2)

	if readService(t, a, a.secret, created.ID).Blocked == "" {
		t.Fatal("it should be stuck at a quota of one")
	}

	setQuota(t, a, "prj-default", `{"instances":10}`)
	settle(t, a, 2)

	held := readService(t, a, a.secret, created.ID)
	if held.Blocked != "" {
		t.Fatalf("blocked = %q, want it cleared once the quota allowed it", held.Blocked)
	}
	if len(held.Members) != 2 {
		t.Fatalf("members = %d, want it to catch up", len(held.Members))
	}
}

func TestReplicaNamesAreUniqueEvenAgainstAHandMadeInstance(t *testing.T) {
	a, _ := newBalancingApp(t)

	newInstance(t, a, a.secret, "web-1")

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 2 {
		t.Fatalf("members = %d, want a hand-made web-1 not to wedge the service",
			len(held.Members))
	}
	if held.Blocked != "" {
		t.Fatalf("blocked = %q, want the name collision avoided rather than survived",
			held.Blocked)
	}
}

func TestAServiceIsInvisibleFromAnotherProject(t *testing.T) {
	a, _ := newBalancingApp(t)

	createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))

	if rec := doAs(t, a, other, http.MethodGet, "/v1/services/web",
		nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	rec := doAs(t, a, other, http.MethodGet, "/v1/services", nil)
	var body struct {
		Services []serviceBody `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Services) != 0 {
		t.Fatalf("services = %d, want none from another project", len(body.Services))
	}
}

func TestAServiceRefusesAnImpossibleShape(t *testing.T) {
	a, _ := newBalancingApp(t)

	refused := map[string]string{
		"no image or iso": `{"name":"a","replicas":1,"isolation":"container"}`,
		"replicas over the max": `{"name":"b","replicas":` +
			strconv.Itoa(1000) + `,"image":"alpine:3.20"}`,
		"negative replicas": `{"name":"c","replicas":-1,"image":"alpine:3.20"}`,
		"an empty name":     `{"name":"","replicas":1,"image":"alpine:3.20"}`,
	}

	for what, body := range refused {
		rec := do(t, a, http.MethodPost, "/v1/services", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d (%s)", what, rec.Code,
				http.StatusBadRequest, rec.Body.String())
		}
	}
}

func TestTwoServicesCannotShareANameInOneProject(t *testing.T) {
	a, _ := newBalancingApp(t)

	createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	rec := do(t, a, http.MethodPost, "/v1/services",
		strings.NewReader(`{"name":"web","replicas":1,"image":"alpine:3.20"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestAViewerCanReadServicesButNotScaleThem(t *testing.T) {
	a, _ := newBalancingApp(t)

	createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"looker","role":"viewer"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if rec := doAs(t, a, created.Secret, http.MethodGet, "/v1/services",
		nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec := doAs(t, a, created.Secret, http.MethodPost, "/v1/services/web/scale",
		strings.NewReader(`{"replicas":5}`)); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer scale = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAServiceCreatesAtMostAFewReplicasPerPass(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":10,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != 4 {
		t.Fatalf("members = %d, want the pass capped so one service cannot storm the scheduler",
			len(held.Members))
	}

	settle(t, a, 2)
	if held := readService(t, a, a.secret, created.ID); len(held.Members) != 10 {
		t.Fatalf("members = %d, want it to reach ten over a few passes", len(held.Members))
	}
}
