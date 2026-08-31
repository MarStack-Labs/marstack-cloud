package app

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

type rolloutBody struct {
	Current int `json:"current"`
	Stale   int `json:"stale"`
}

type revisionMember struct {
	InstanceID string `json:"instance_id"`
	Revision   int    `json:"revision"`
}

type revisionBody struct {
	ID       string           `json:"id"`
	Revision int              `json:"revision"`
	Image    string           `json:"image"`
	Blocked  string           `json:"blocked"`
	Rollout  rolloutBody      `json:"rollout"`
	Members  []revisionMember `json:"members"`
}

func retemplate(t *testing.T, a *testApp, id, body string) (int, revisionBody) {
	t.Helper()

	rec := do(t, a, http.MethodPut, "/v1/services/"+id+"/template", strings.NewReader(body))

	var got revisionBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got
}

func revisionOf(t *testing.T, a *testApp, id string) revisionBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/services/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read service: %d %s", rec.Code, rec.Body.String())
	}

	var body revisionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func memberIDs(body revisionBody) []string {
	ids := make([]string, 0, len(body.Members))
	for _, member := range body.Members {
		ids = append(ids, member.InstanceID)
	}
	sort.Strings(ids)
	return ids
}

func TestANewServiceStartsAtRevisionOne(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	held := revisionOf(t, a, created.ID)
	if held.Revision != 1 {
		t.Fatalf("revision = %d, want 1", held.Revision)
	}
	if held.Rollout.Current != 2 || held.Rollout.Stale != 0 {
		t.Fatalf("rollout = %+v, want both replicas counted as current", held.Rollout)
	}
}

func TestPublishingATemplateBumpsTheRevision(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	code, updated := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21"}`)
	if code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}
	if updated.Revision != 2 {
		t.Fatalf("revision = %d, want 2", updated.Revision)
	}
	if updated.Image != "alpine:3.21" {
		t.Fatalf("image = %q, want the one just published", updated.Image)
	}
	if updated.Rollout.Stale != 2 || updated.Rollout.Current != 0 {
		t.Fatalf("rollout = %+v, want both replicas stale before the loop has run",
			updated.Rollout)
	}
}

func TestReplicasAreReplacedOneAtATime(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":3,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)
	before := memberIDs(revisionOf(t, a, created.ID))

	if code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21"}`); code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}

	for pass, want := range []int{1, 2, 3} {
		settle(t, a, 1)

		held := revisionOf(t, a, created.ID)
		if len(held.Members) != 3 {
			t.Fatalf("pass %d: members = %d, want the service to stay at three by the end "+
				"of every pass", pass+1, len(held.Members))
		}
		if held.Rollout.Current != want {
			t.Fatalf("pass %d: current = %d, want %d: one replica is replaced per pass, so "+
				"a broken revision costs one replica rather than the whole service",
				pass+1, held.Rollout.Current, want)
		}
	}

	after := memberIDs(revisionOf(t, a, created.ID))
	for _, id := range after {
		for _, old := range before {
			if id == old {
				t.Fatalf("instance %s survived the rollout: a replica on the old revision is "+
					"still running the old image, so replacing it is the whole point", id)
			}
		}
	}
	if instanceCount(t, a) != 3 {
		t.Fatalf("instances = %d, want three: the retired replicas were really deleted",
			instanceCount(t, a))
	}
}

func TestSettledReplicasAreLeftAlone(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	if code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21"}`); code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}
	settle(t, a, 4)
	settled := memberIDs(revisionOf(t, a, created.ID))

	settle(t, a, 6)
	again := revisionOf(t, a, created.ID)

	if strings.Join(settled, ",") != strings.Join(memberIDs(again), ",") {
		t.Fatalf("members were %v and are now %v: once every replica is on the current "+
			"revision the loop has nothing left to do", settled, memberIDs(again))
	}
	if again.Rollout.Stale != 0 {
		t.Fatalf("stale = %d, want none after the rollout finished", again.Rollout.Stale)
	}
}

func TestABrokenRevisionStopsAfterOneReplica(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":3,"isolation":"container","image":"alpine:3.20","vcpu":1}`)
	settle(t, a, 1)
	setQuota(t, a, "prj-default", `{"vcpu":3}`)

	if code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21","vcpu":3}`,
	); code != http.StatusOK {
		t.Fatalf("publish: %d: a revision the project cannot afford is still a revision, "+
			"because a quota is about the whole project rather than this template", code)
	}

	settle(t, a, 8)

	held := revisionOf(t, a, created.ID)
	if held.Blocked == "" {
		t.Fatal("the service is short of replicas and says nothing about why")
	}
	if len(held.Members) != 2 {
		t.Fatalf("members = %d, want 2: one replica is retired to make room, and once the "+
			"replacement cannot be made the rollout must not retire another", len(held.Members))
	}
	if held.Rollout.Stale != 2 {
		t.Fatalf("stale = %d, want the two survivors still serving on the old revision",
			held.Rollout.Stale)
	}
}

func TestRollingBackIsJustAnotherRevision(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	if code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21"}`); code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}
	settle(t, a, 4)

	if code, back := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.20"}`); code != http.StatusOK {
		t.Fatalf("roll back: %d", code)
	} else if back.Revision != 3 {
		t.Fatalf("revision = %d, want 3: a rollback moves forward, because two replicas on "+
			"\"revision 1\" that were made at different times are not the same thing",
			back.Revision)
	}
	settle(t, a, 4)

	held := revisionOf(t, a, created.ID)
	if held.Image != "alpine:3.20" || held.Rollout.Stale != 0 {
		t.Fatalf("image = %q with %d stale, want the old image fully rolled out",
			held.Image, held.Rollout.Stale)
	}
}

func TestScalingDownDropsTheStaleReplicasFirst(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":4,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	if code, _ := retemplate(t, a, created.ID,
		`{"isolation":"container","image":"alpine:3.21"}`); code != http.StatusOK {
		t.Fatalf("publish: %d", code)
	}
	settle(t, a, 2)

	rec := do(t, a, http.MethodPost, "/v1/services/"+created.ID+"/scale",
		strings.NewReader(`{"replicas":2}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("scale: %d %s", rec.Code, rec.Body.String())
	}
	settle(t, a, 1)

	held := revisionOf(t, a, created.ID)
	if len(held.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(held.Members))
	}
	if held.Rollout.Stale != 0 {
		t.Fatalf("stale = %d, want none: there were two of each and two had to go, so "+
			"cutting the ones already due for replacement finishes the rollout for free",
			held.Rollout.Stale)
	}
}

func TestATemplateWithNoImageIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	if code, _ := retemplate(t, a, created.ID, `{"isolation":"container"}`); code !=
		http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a revision that cannot make a replica would retire "+
			"a working one for nothing", code, http.StatusBadRequest)
	}
}

func TestPublishingToAnotherProjectsServiceIsNotFound(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":1,"isolation":"container","image":"alpine:3.20"}`)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, other, http.MethodPut, "/v1/services/"+created.ID+"/template",
		strings.NewReader(`{"isolation":"container","image":"alpine:3.21"}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
