package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type backendBody struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address"`
	Healthy    bool   `json:"healthy"`
}

type balancerBody struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Protocol   string        `json:"protocol"`
	ListenPort int           `json:"listen_port"`
	TargetPort int           `json:"target_port"`
	Algorithm  string        `json:"algorithm"`
	Backends   []backendBody `json:"backends"`
}

func registerNode(t *testing.T, a *testApp, name, zone string) string {
	t.Helper()

	rec := post(t, a, "/v1/nodes/register", `{"name":"`+name+`","zone":"`+zone+
		`","arch":"arm64","os":"linux","cpus":4,"memory_mib":6144,"agent_version":"test"}`)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("register node: %d %s", rec.Code, rec.Body.String())
	}

	var registered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	return registered.ID
}

func runningInstance(t *testing.T, a *testApp, secret, name, nodeID string) string {
	t.Helper()

	id := newInstance(t, a, secret, name)
	if placed := waitForPlacement(t, a, id); placed == "" {
		t.Fatalf("instance %s was never placed on a node", name)
	}

	rec := do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/instances/"+id+"/status",
		strings.NewReader(`{"observed_state":"running"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("report running: %d %s", rec.Code, rec.Body.String())
	}
	return id
}

func createBalancer(t *testing.T, a *testApp, secret, body string) balancerBody {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodPost, "/v1/balancers", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create balancer: %d %s", rec.Code, rec.Body.String())
	}

	var created balancerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode balancer: %v", err)
	}
	return created
}

func nodeBalancers(t *testing.T, a *testApp, nodeID string) []balancerBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/balancers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node balancers: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Balancers []balancerBody `json:"balancers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Balancers
}

func newBalancingApp(t *testing.T) (*testApp, string) {
	t.Helper()

	a := newSchedulingApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.scheduler.Run(ctx)

	return a, registerNode(t, a, "bm-1", "rack-a")
}

func TestABalancerCarriesEveryRunningBackend(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	first := runningInstance(t, a, a.secret, "web-1", nodeID)
	second := runningInstance(t, a, a.secret, "web-2", nodeID)

	created := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,
		"listen_port":8080,"instances":["`+first+`","`+second+`"]}`)

	if len(created.Backends) != 2 {
		t.Fatalf("backends = %d, want the two instances given", len(created.Backends))
	}
	for _, backend := range created.Backends {
		if !backend.Healthy {
			t.Fatalf("backend %s is not healthy, want a running instance to take traffic",
				backend.InstanceID)
		}
		if backend.Address == "" {
			t.Fatalf("backend %s has no address, so nothing could be rewritten to it",
				backend.InstanceID)
		}
	}
}

func TestANodeOnlySeesBackendsThatAreRunning(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	up := runningInstance(t, a, a.secret, "web-1", nodeID)
	pending := newInstance(t, a, a.secret, "web-2")

	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,
		"listen_port":8080,"instances":["`+up+`","`+pending+`"]}`)

	balancers := nodeBalancers(t, a, nodeID)
	if len(balancers) != 1 {
		t.Fatalf("balancers = %d, want the one that has a live backend", len(balancers))
	}
	if len(balancers[0].Backends) != 1 {
		t.Fatalf("backends = %d, want only the running one", len(balancers[0].Backends))
	}
	if balancers[0].Backends[0].InstanceID != up {
		t.Fatalf("backend = %s, want %s", balancers[0].Backends[0].InstanceID, up)
	}
}

func TestABalancerWithNothingRunningIsNotSentToANode(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	pending := newInstance(t, a, a.secret, "web-1")
	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,
		"listen_port":8080,"instances":["`+pending+`"]}`)

	if balancers := nodeBalancers(t, a, nodeID); len(balancers) != 0 {
		t.Fatalf("balancers = %d, want none while every backend is down", len(balancers))
	}
}

func TestABalancerRefusesAPortAPublishedForwardHolds(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)

	rec := do(t, a, http.MethodPost, "/v1/forwards",
		strings.NewReader(`{"instance_id":"`+id+`","target_port":80,"node_port":8080}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create forward: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, a, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"web","target_port":80,"listen_port":8080}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d because a balancer claims the port on every node",
			rec.Code, http.StatusConflict)
	}
}

func TestAPublishedForwardRefusesAPortABalancerHolds(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080}`)

	rec := do(t, a, http.MethodPost, "/v1/forwards",
		strings.NewReader(`{"instance_id":"`+id+`","target_port":80,"node_port":8080}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d because the balancer already holds 8080 everywhere",
			rec.Code, http.StatusConflict)
	}
}

func TestTwoBalancersCannotShareAListenPort(t *testing.T) {
	a, _ := newBalancingApp(t)

	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080}`)

	rec := do(t, a, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"api","target_port":90,"listen_port":8080}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestABalancerIsInvisibleFromAnotherProject(t *testing.T) {
	a, _ := newBalancingApp(t)

	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080}`)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))

	rec := doAs(t, a, other, http.MethodGet, "/v1/balancers/web", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d so the name itself stays private",
			rec.Code, http.StatusNotFound)
	}

	rec = doAs(t, a, other, http.MethodGet, "/v1/balancers", nil)
	var body struct {
		Balancers []balancerBody `json:"balancers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Balancers) != 0 {
		t.Fatalf("balancers = %d, want none from another project", len(body.Balancers))
	}
}

func TestAnInstanceFromAnotherProjectCannotBecomeABackend(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	mine := runningInstance(t, a, a.secret, "web-1", nodeID)
	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))

	rec := doAs(t, a, other, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"web","target_port":80,"listen_port":8081,
			"instances":["`+mine+`"]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDeletingAnInstanceTakesItOutOfItsBalancer(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	first := runningInstance(t, a, a.secret, "web-1", nodeID)
	second := runningInstance(t, a, a.secret, "web-2", nodeID)

	created := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,
		"listen_port":8080,"instances":["`+first+`","`+second+`"]}`)

	rec := do(t, a, http.MethodDelete, "/v1/instances/"+first, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete instance: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, a, http.MethodGet, "/v1/balancers/"+created.ID, nil)
	var after balancerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(after.Backends) != 1 || after.Backends[0].InstanceID != second {
		t.Fatalf("backends = %v, want only %s left", after.Backends, second)
	}
}

func TestABackendCanBeAddedAndRemovedAfterwards(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	first := runningInstance(t, a, a.secret, "web-1", nodeID)
	second := runningInstance(t, a, a.secret, "web-2", nodeID)

	created := createBalancer(t, a, a.secret,
		`{"name":"web","target_port":80,"listen_port":8080,"instances":["`+first+`"]}`)

	rec := do(t, a, http.MethodPost, "/v1/balancers/"+created.ID+"/backends",
		strings.NewReader(`{"instance_id":"`+second+`"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("add backend: %d %s", rec.Code, rec.Body.String())
	}

	var grown balancerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &grown); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(grown.Backends) != 2 {
		t.Fatalf("backends = %d, want two after adding one", len(grown.Backends))
	}

	rec = do(t, a, http.MethodDelete, "/v1/balancers/web/backends/"+second, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove backend: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, a, http.MethodGet, "/v1/balancers/web", nil)
	var shrunk balancerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &shrunk); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(shrunk.Backends) != 1 {
		t.Fatalf("backends = %d, want one after removing the other", len(shrunk.Backends))
	}
}

func TestTheSameBackendNamedTwiceIsHeldOnce(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	created := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,
		"listen_port":8080,"instances":["`+id+`","`+id+`"]}`)

	if len(created.Backends) != 1 {
		t.Fatalf("backends = %d, want the duplicate collapsed", len(created.Backends))
	}
}

func TestAnUnknownAlgorithmIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	rec := do(t, a, http.MethodPost, "/v1/balancers", strings.NewReader(
		`{"name":"web","target_port":80,"listen_port":8080,"algorithm":"least_conn"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDeletingABalancerReleasesItsListenPort(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createBalancer(t, a, a.secret,
		`{"name":"web","target_port":80,"listen_port":8080}`)

	rec := do(t, a, http.MethodDelete, "/v1/balancers/"+created.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	createBalancer(t, a, a.secret, `{"name":"api","target_port":90,"listen_port":8080}`)
}

func TestAViewerCanReadBalancersButNotChangeThem(t *testing.T) {
	a, _ := newBalancingApp(t)

	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080}`)

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"looker","role":"viewer"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create viewer token: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if rec := doAs(t, a, created.Secret, http.MethodGet, "/v1/balancers",
		nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer list = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec := doAs(t, a, created.Secret, http.MethodDelete, "/v1/balancers/web",
		nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer delete = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
