package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type healthMember struct {
	InstanceID string `json:"instance_id"`
	State      string `json:"state"`
}

type healthBody struct {
	Blocked string `json:"blocked"`
	Rollout struct {
		Current int `json:"current"`
		Stale   int `json:"stale"`
		Failed  int `json:"failed"`
	} `json:"rollout"`
	Members []healthMember `json:"members"`
}

func healthOf(t *testing.T, a *testApp, id string) healthBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/services/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read service: %d %s", rec.Code, rec.Body.String())
	}

	var body healthBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func observe(t *testing.T, a *testApp, nodeID, instanceID, state string) {
	t.Helper()
	reportState(t, a, nodeID, instanceID, `{"observed_state":"`+state+`"}`)
}

func placedMembers(t *testing.T, a *testApp, nodeID, id string) []string {
	t.Helper()

	held := healthOf(t, a, id)
	ids := make([]string, 0, len(held.Members))
	for _, member := range held.Members {
		if placed := waitForPlacement(t, a, member.InstanceID); placed == "" {
			t.Fatalf("replica %s was never placed", member.InstanceID)
		}
		ids = append(ids, member.InstanceID)
	}
	return ids
}

func TestAFailedReplicaIsReplaced(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	members := placedMembers(t, a, nodeID, created.ID)
	for _, id := range members {
		observe(t, a, nodeID, id, "running")
	}
	observe(t, a, nodeID, members[0], "failed")

	seen := healthOf(t, a, created.ID)
	if seen.Rollout.Failed != 1 {
		t.Fatalf("failed = %d, want 1 reported before anything is done about it: a service "+
			"that says 2/2 up while one replica serves nothing is the whole bug",
			seen.Rollout.Failed)
	}

	settle(t, a, 2)

	after := healthOf(t, a, created.ID)
	if len(after.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(after.Members))
	}
	for _, member := range after.Members {
		if member.InstanceID == members[0] {
			t.Fatal("the failed replica is still a member, so the count still lies")
		}
	}
}

func TestARunningReplicaIsLeftAlone(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	members := placedMembers(t, a, nodeID, created.ID)
	for _, id := range members {
		observe(t, a, nodeID, id, "running")
	}
	settle(t, a, 5)

	after := healthOf(t, a, created.ID)
	for _, member := range after.Members {
		if member.State != "running" {
			t.Fatalf("replica %s is %q, want running", member.InstanceID, member.State)
		}
	}
	if after.Rollout.Failed != 0 {
		t.Fatalf("failed = %d, want none", after.Rollout.Failed)
	}
}

func TestAStoppedReplicaIsReportedButNotReplaced(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	members := placedMembers(t, a, nodeID, created.ID)
	observe(t, a, nodeID, members[0], "stopped")
	settle(t, a, 4)

	after := healthOf(t, a, created.ID)
	for _, member := range after.Members {
		if member.InstanceID == members[0] {
			if member.State != "stopped" {
				t.Fatalf("state = %q, want stopped so an operator can see it", member.State)
			}
			return
		}
	}
	t.Fatal("a replica somebody stopped on purpose was deleted: the platform's own verdict " +
		"is failed, and stopped is a decision a person made")
}

func TestReplacingAFailedReplicaGivesUpRatherThanChurning(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	for range 12 {
		settle(t, a, 1)

		held := healthOf(t, a, created.ID)
		for _, member := range held.Members {
			if placed := waitForPlacement(t, a, member.InstanceID); placed != "" {
				observe(t, a, nodeID, member.InstanceID, "failed")
			}
		}
	}

	held := healthOf(t, a, created.ID)
	if held.Blocked == "" {
		t.Fatal("the service kept making replicas that keep failing and never said so: " +
			"replacing without a bound is a crashloop the platform runs on your behalf")
	}
	if !strings.Contains(held.Blocked, "keep failing") {
		t.Fatalf("blocked = %q, want it to name the reason", held.Blocked)
	}
	if len(held.Members) != 1 {
		t.Fatalf("members = %d, want the last one left alone rather than deleted for a "+
			"replacement that will fail too", len(held.Members))
	}
}

func TestAPendingReplacementDoesNotCountAsRecovered(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	members := placedMembers(t, a, nodeID, created.ID)
	healthy := members[0]
	observe(t, a, nodeID, healthy, "running")

	for range 8 {
		for _, member := range healthOf(t, a, created.ID).Members {
			if member.InstanceID == healthy || member.State == "failed" {
				continue
			}
			if placed := waitForPlacement(t, a, member.InstanceID); placed != "" {
				observe(t, a, nodeID, member.InstanceID, "failed")
			}
		}

		settle(t, a, 1)
		settle(t, a, 1)
	}

	held := healthOf(t, a, created.ID)
	if held.Blocked == "" {
		t.Fatal("one replica stayed healthy while every replacement for the other failed, " +
			"and the service never gave up. The pass after a replacement sees the new " +
			"replica still pending: that is not a recovery, and counting it as one clears " +
			"the tally every other pass so the limit is never reached")
	}
}

func TestAFixedRevisionUnsticksAServiceThatGaveUp(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	for range 10 {
		settle(t, a, 1)
		for _, id := range placedMembers(t, a, nodeID, created.ID) {
			observe(t, a, nodeID, id, "failed")
		}
	}
	if healthOf(t, a, created.ID).Blocked == "" {
		t.Fatal("the service never gave up, so there is nothing to recover from")
	}

	if code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21"}`); code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}

	for range 4 {
		settle(t, a, 1)
		for _, member := range healthOf(t, a, created.ID).Members {
			if member.State != "pending" {
				continue
			}
			if placed := waitForPlacement(t, a, member.InstanceID); placed != "" {
				observe(t, a, nodeID, member.InstanceID, "running")
			}
		}
	}
	settle(t, a, 1)

	held := healthOf(t, a, created.ID)
	if held.Blocked != "" {
		t.Fatalf("blocked = %q, want nothing: publishing a revision is the operator saying "+
			"the template is fixed, and without clearing the tally a service that gave up "+
			"can never be recovered except by deleting it", held.Blocked)
	}
	if held.Rollout.Failed != 0 || len(held.Members) != 1 {
		t.Fatalf("rollout = %+v with %d members, want one healthy replica on the new "+
			"revision", held.Rollout, len(held.Members))
	}
}

func TestAServiceThatRecoversForgetsTheAttempts(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	for range 2 {
		settle(t, a, 1)
		for _, id := range placedMembers(t, a, nodeID, created.ID) {
			observe(t, a, nodeID, id, "failed")
		}
	}

	settle(t, a, 1)
	for _, id := range placedMembers(t, a, nodeID, created.ID) {
		observe(t, a, nodeID, id, "running")
	}
	settle(t, a, 2)

	for range 3 {
		for _, id := range placedMembers(t, a, nodeID, created.ID) {
			observe(t, a, nodeID, id, "failed")
		}
		settle(t, a, 1)
	}

	held := healthOf(t, a, created.ID)
	if held.Blocked != "" {
		t.Fatalf("blocked = %q, want nothing: the count is about a service that cannot get "+
			"healthy, so reaching healthy has to clear it or a service is condemned by "+
			"failures it already recovered from", held.Blocked)
	}
}
