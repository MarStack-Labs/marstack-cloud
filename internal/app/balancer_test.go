package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type backendBody struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address"`
	Healthy    bool   `json:"healthy"`
	Running    bool   `json:"running"`
	Probe      string `json:"probe"`
	Reason     string `json:"reason"`
	CheckedAt  string `json:"checked_at"`
}

type balancerBody struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Protocol   string              `json:"protocol"`
	ListenPort int                 `json:"listen_port"`
	TargetPort int                 `json:"target_port"`
	Algorithm  string              `json:"algorithm"`
	Service    string              `json:"service"`
	Check      string              `json:"check"`
	CheckPath  string              `json:"check_path"`
	Rise       int                 `json:"rise"`
	Fall       int                 `json:"fall"`
	Backends   []backendBody       `json:"backends"`
	Routes     []balancerRouteBody `json:"routes"`
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

func TestANodeSeesTheWholeMembershipWithHealthPerBackend(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	up := runningInstance(t, a, a.secret, "web-1", nodeID)
	pending := newInstance(t, a, a.secret, "web-2")

	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,
		"listen_port":8080,"instances":["`+up+`","`+pending+`"]}`)

	balancers := nodeBalancers(t, a, nodeID)
	if len(balancers) != 1 {
		t.Fatalf("balancers = %d, want the one that exists", len(balancers))
	}

	if len(balancers[0].Backends) != 2 {
		t.Fatalf("backends = %d, want both so the node knows what to probe",
			len(balancers[0].Backends))
	}

	byID := map[string]backendBody{}
	for _, backend := range balancers[0].Backends {
		byID[backend.InstanceID] = backend
	}
	if !byID[up].Healthy {
		t.Fatalf("the running backend is not marked healthy: %+v", byID[up])
	}
	if byID[pending].Healthy {
		t.Fatalf("the pending backend is marked healthy: %+v", byID[pending])
	}
}

func TestABalancerWithNothingRunningCarriesNoHealthyBackend(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	pending := newInstance(t, a, a.secret, "web-1")
	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,
		"listen_port":8080,"instances":["`+pending+`"]}`)

	balancers := nodeBalancers(t, a, nodeID)
	if len(balancers) != 1 {
		t.Fatalf("balancers = %d, want the membership to still reach the node", len(balancers))
	}
	for _, backend := range balancers[0].Backends {
		if backend.Healthy {
			t.Fatalf("backend %s is healthy while nothing is running", backend.InstanceID)
		}
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

func reportHealth(t *testing.T, a *testApp, nodeID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/balancers/health",
		strings.NewReader(body))
}

func backendOf(t *testing.T, a *testApp, name, instanceID string) backendBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/balancers/"+name, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get balancer: %d %s", rec.Code, rec.Body.String())
	}

	var b balancerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, backend := range b.Backends {
		if backend.InstanceID == instanceID {
			return backend
		}
	}
	t.Fatalf("instance %s is not a backend of %s", instanceID, name)
	return backendBody{}
}

func TestWithoutACheckABalancerTrustsVMLiveness(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	created := createBalancer(t, a, a.secret,
		`{"name":"web","target_port":80,"listen_port":8080,"instances":["`+id+`"]}`)

	if created.Check != "none" {
		t.Fatalf("check = %q, want none by default", created.Check)
	}

	backend := backendOf(t, a, "web", id)
	if !backend.Healthy || backend.Probe != "" {
		t.Fatalf("backend = %+v, want healthy with no probe state", backend)
	}
}

func TestACheckedBackendIsNotHealthyUntilANodeReports(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"tcp","instances":["`+id+`"]}`)

	backend := backendOf(t, a, "web", id)
	if backend.Healthy {
		t.Fatal("a checked backend is healthy before any probe has run")
	}
	if !backend.Running {
		t.Fatal("the instance is running, so running should still be true")
	}
	if backend.Probe != "unknown" {
		t.Fatalf("probe = %q, want unknown", backend.Probe)
	}
}

func TestAPassingReportBringsACheckedBackendUp(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	lb := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"tcp","instances":["`+id+`"]}`)

	rec := reportHealth(t, a, nodeID, `{"checks":[{"balancer_id":"`+lb.ID+
		`","instance_id":"`+id+`","healthy":true}]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	backend := backendOf(t, a, "web", id)
	if !backend.Healthy || backend.Probe != "passing" {
		t.Fatalf("backend = %+v, want healthy and passing", backend)
	}
	if backend.CheckedAt == "" {
		t.Fatal("checked_at is empty, so staleness could never be judged")
	}
}

func TestAFailingReportTakesABackendOutWhileItKeepsRunning(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	lb := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"http","check_path":"/healthz","instances":["`+id+`"]}`)

	reportHealth(t, a, nodeID, `{"checks":[{"balancer_id":"`+lb.ID+
		`","instance_id":"`+id+`","healthy":false,"reason":"http status 500"}]}`)

	backend := backendOf(t, a, "web", id)
	if backend.Healthy {
		t.Fatal("a backend whose check fails is still healthy")
	}
	if !backend.Running {
		t.Fatal("running should stay true: the point is that VM liveness cannot see this")
	}
	if backend.Probe != "failing" || backend.Reason != "http status 500" {
		t.Fatalf("backend = %+v, want the failure and its reason", backend)
	}
}

func TestANodeCannotReportForAnInstanceItDoesNotHold(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	lb := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"tcp","instances":["`+id+`"]}`)

	other := registerNode(t, a, "bm-2", "rack-b")
	rec := reportHealth(t, a, other, `{"checks":[{"balancer_id":"`+lb.ID+
		`","instance_id":"`+id+`","healthy":true}]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	if backend := backendOf(t, a, "web", id); backend.Probe != "unknown" {
		t.Fatalf("probe = %q, want the report from the wrong node ignored", backend.Probe)
	}
}

func TestAReportForSomethingThatIsNotABackendIsIgnored(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	stranger := runningInstance(t, a, a.secret, "web-2", nodeID)
	lb := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"tcp","instances":["`+id+`"]}`)

	rec := reportHealth(t, a, nodeID, `{"checks":[{"balancer_id":"`+lb.ID+
		`","instance_id":"`+stranger+`","healthy":true}]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, a, http.MethodGet, "/v1/balancers/web", nil)
	var b balancerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(b.Backends) != 1 {
		t.Fatalf("backends = %d, want a report not to invent one", len(b.Backends))
	}
}

func TestReAddingABackendDoesNotInheritItsOldVerdict(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	lb := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"tcp","instances":["`+id+`"]}`)

	reportHealth(t, a, nodeID, `{"checks":[{"balancer_id":"`+lb.ID+
		`","instance_id":"`+id+`","healthy":true}]}`)
	if backend := backendOf(t, a, "web", id); !backend.Healthy {
		t.Fatal("the backend never came up")
	}

	rec := do(t, a, http.MethodDelete, "/v1/balancers/"+lb.ID+"/backends/"+id, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, a, http.MethodPost, "/v1/balancers/"+lb.ID+"/backends",
		strings.NewReader(`{"instance_id":"`+id+`"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("add back: %d %s", rec.Code, rec.Body.String())
	}

	backend := backendOf(t, a, "web", id)
	if backend.Healthy || backend.Probe != "unknown" {
		t.Fatalf("backend = %+v, want it unverified again rather than trusted", backend)
	}
}

func TestCheckConfigurationIsValidated(t *testing.T) {
	a, _ := newBalancingApp(t)

	refused := map[string]string{
		"an unknown check kind":      `{"name":"a","target_port":80,"check":"ping"}`,
		"a path without a check":     `{"name":"b","target_port":80,"check_path":"/x"}`,
		"a path that is not a path":  `{"name":"c","target_port":80,"check":"http","check_path":"x"}`,
		"a rise without a check":     `{"name":"d","target_port":80,"rise":3}`,
		"a rise over the maximum":    `{"name":"e","target_port":80,"check":"tcp","rise":99}`,
		"a fall below one":           `{"name":"f","target_port":80,"check":"tcp","fall":-1}`,
		"an http check over udp":     `{"name":"g","target_port":80,"protocol":"udp","check":"http"}`,
		"a path carrying whitespace": `{"name":"h","target_port":80,"check":"http","check_path":"/a b"}`,
	}

	for what, body := range refused {
		rec := do(t, a, http.MethodPost, "/v1/balancers", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d (%s)", what, rec.Code,
				http.StatusBadRequest, rec.Body.String())
		}
	}
}

func TestAnHTTPCheckDefaultsItsPathAndThresholds(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createBalancer(t, a, a.secret,
		`{"name":"web","target_port":80,"listen_port":8080,"check":"http"}`)

	if created.CheckPath != "/" {
		t.Fatalf("check_path = %q, want /", created.CheckPath)
	}
	if created.Rise != 2 || created.Fall != 2 {
		t.Fatalf("rise/fall = %d/%d, want 2/2", created.Rise, created.Fall)
	}
}
