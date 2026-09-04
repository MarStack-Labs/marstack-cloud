package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type balancerRouteBody struct {
	Host     string        `json:"host"`
	Path     string        `json:"path"`
	Service  string        `json:"service"`
	Backends []backendBody `json:"backends"`
}

func routedBalancer(t *testing.T, a *testApp, nodeID, body string) balancerBody {
	t.Helper()

	created := createBalancer(t, a, a.secret, body)
	for _, b := range nodeBalancers(t, a, nodeID) {
		if b.ID == created.ID {
			return b
		}
	}

	t.Fatalf("balancer %s is not in the node view", created.ID)
	return balancerBody{}
}

func runningService(t *testing.T, a *testApp, nodeID, name string, replicas int) []string {
	t.Helper()

	created := createService(t, a, a.secret, `{"name":"`+name+`","replicas":`+
		strconv.Itoa(replicas)+`,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)

	held := readService(t, a, a.secret, created.ID)
	if len(held.Members) != replicas {
		t.Fatalf("service %s holds %d replicas, want %d", name, len(held.Members), replicas)
	}

	ids := make([]string, 0, replicas)
	for _, member := range held.Members {
		reportRunning(t, a, nodeID, member.InstanceID)
		ids = append(ids, member.InstanceID)
	}
	return ids
}

func routeOf(t *testing.T, b balancerBody, host, path string) balancerRouteBody {
	t.Helper()

	for _, route := range b.Routes {
		if route.Host == host && route.Path == path {
			return route
		}
	}

	t.Fatalf("no route for %q%q in %+v", host, path, b.Routes)
	return balancerRouteBody{}
}

func refuseBalancer(t *testing.T, a *testApp, body string) string {
	t.Helper()

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/balancers", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest,
			rec.Body.String())
	}
	return rec.Body.String()
}

func TestOnePortServesTwoServicesByHostname(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	web := runningService(t, a, nodeID, "web", 1)
	api := runningService(t, a, nodeID, "api", 1)

	held := routedBalancer(t, a, nodeID, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[
			{"host":"app.test","service":"web"},
			{"host":"api.test","service":"api"}]}`)

	for name, ids := range map[string][]string{"app.test": web, "api.test": api} {
		route := routeOf(t, held, name, "")
		if len(route.Backends) != 1 {
			t.Fatalf("%s carries %d backends, want the one replica of its service",
				name, len(route.Backends))
		}
		if route.Backends[0].InstanceID != ids[0] {
			t.Fatalf("%s goes to %s, want %s: a route that resolves to the wrong service "+
				"sends one tenant's traffic to another",
				name, route.Backends[0].InstanceID, ids[0])
		}
		if route.Backends[0].Address == "" {
			t.Fatalf("%s has no address on its backend, so the node has nothing to dial",
				name)
		}
		if !route.Backends[0].Healthy {
			t.Fatalf("%s has an unhealthy backend, and a running replica takes traffic",
				name)
		}
	}
}

func TestARouteFollowsItsServiceAsItScales(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[{"host":"app.test","service":"web"}]}`)

	rec := do(t, a, http.MethodPost, "/v1/services/web/scale",
		strings.NewReader(`{"replicas":3}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("scale: %d %s", rec.Code, rec.Body.String())
	}
	settle(t, a, 1)

	for _, b := range nodeBalancers(t, a, nodeID) {
		if b.ID != created.ID {
			continue
		}
		if route := routeOf(t, b, "app.test", ""); len(route.Backends) != 3 {
			t.Fatalf("the route carries %d backends, want three: membership is derived on "+
				"read, so scaling a service must move traffic with nothing registered by hand",
				len(route.Backends))
		}
		return
	}
	t.Fatal("the balancer left the node view")
}

func TestTheLongestPathOfTheRightHostAnswersFirst(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)
	runningService(t, a, nodeID, "api", 1)

	held := routedBalancer(t, a, nodeID, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[
			{"path":"/","service":"web"},
			{"host":"app.test","path":"/api/v2","service":"api"},
			{"host":"app.test","service":"web"},
			{"host":"app.test","path":"/api","service":"api"}]}`)

	order := make([]string, 0, len(held.Routes))
	for _, route := range held.Routes {
		order = append(order, route.Host+"|"+route.Path)
	}

	want := []string{"app.test|/api/v2", "app.test|/api", "app.test|", "|"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("routes came back as %v, want %v: the order is the rule, and a node that "+
			"walks them in the order they were typed answers a request from whichever one "+
			"happened to be first", order, want)
	}
}

func TestTwoRoutesForOneHostAndPathAreRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)
	runningService(t, a, nodeID, "api", 1)

	body := refuseBalancer(t, a, `{"name":"edge","target_port":80,"listen_port":8443,
		"routes":[
			{"host":"app.test","path":"/api/","service":"web"},
			{"host":"APP.TEST","path":"/api","service":"api"}]}`)

	if !strings.Contains(body, "two answers") {
		t.Fatalf("refusal = %s, want it to name the ambiguity: a trailing slash and a "+
			"capital letter are the same route", body)
	}
}

func TestARouteToAServiceThatDoesNotExistIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	body := refuseBalancer(t, a, `{"name":"edge","target_port":80,"listen_port":8443,
		"routes":[{"host":"app.test","service":"nothing"}]}`)

	if !strings.Contains(body, "404") {
		t.Fatalf("refusal = %s, want it to say what the route would answer", body)
	}
}

func TestARouteInAnotherProjectIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"edge","target_port":80,"listen_port":8444,
			"routes":[{"host":"app.test","service":"web"}]}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a service in another project answers exactly the "+
			"way one that does not exist answers", rec.Code, http.StatusBadRequest)
	}
}

func TestRoutesCannotBeCombinedWithOneSetOfBackends(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)
	instance := runningInstance(t, a, a.secret, "alone", nodeID)

	for _, body := range []string{
		`{"name":"edge","target_port":80,"listen_port":8443,"service":"web",
			"routes":[{"host":"app.test","service":"web"}]}`,
		`{"name":"edge","target_port":80,"listen_port":8443,"instances":["` + instance + `"],
			"routes":[{"host":"app.test","service":"web"}]}`,
	} {
		if refused := refuseBalancer(t, a, body); !strings.Contains(refused, "both ways") {
			t.Errorf("refusal = %s, want it to name the ambiguity", refused)
		}
	}
}

func TestARouteNeedsSomethingToRead(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	if refused := refuseBalancer(t, a, `{"name":"edge","target_port":53,
		"listen_port":8453,"protocol":"udp",
		"routes":[{"host":"app.test","service":"web"}]}`); !strings.Contains(refused,
		"datagram") {
		t.Errorf("refusal = %s, want udp named", refused)
	}

	if refused := refuseBalancer(t, a, `{"name":"edge","target_port":80,
		"listen_port":8443,"algorithm":"source_hash",
		"routes":[{"host":"app.test","service":"web"}]}`); !strings.Contains(refused,
		"ignored") {
		t.Errorf("refusal = %s, want source_hash refused rather than accepted and dropped",
			refused)
	}
}

func TestAMalformedRouteIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	for _, routes := range []string{
		`[{"host":"app.test"}]`,
		`[{"host":"not a host","service":"web"}]`,
		`[{"host":"app.test","path":"api","service":"web"}]`,
		`[{"host":"app.test","path":"/` + strings.Repeat("a", 400) + `","service":"web"}]`,
		`[{"host":"` + strings.Repeat("a", 260) + `.test","service":"web"}]`,
	} {
		refuseBalancer(t, a, `{"name":"edge","target_port":80,"listen_port":8443,
			"routes":`+routes+`}`)
	}
}

func TestMoreRoutesThanABalancerHoldsAreRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	routes := make([]string, 0, 40)
	for i := range 40 {
		routes = append(routes,
			`{"host":"h`+strconv.Itoa(i)+`.test","service":"web"}`)
	}

	if refused := refuseBalancer(t, a, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[`+strings.Join(routes, ",")+`]}`); !strings.Contains(
		refused, "at most") {
		t.Fatalf("refusal = %s, want a bound: a route table nobody caps is a table a "+
			"caller can grow until the node cannot hold it", refused)
	}
}

func TestARoutingBalancerSurvivesARead(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[
			{"host":"app.test","path":"/api","service":"web"}]}`)

	rec := doAs(t, a, a.secret, http.MethodGet, "/v1/balancers/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body.String())
	}

	var held balancerBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(held.Routes) != 1 {
		t.Fatalf("routes = %+v, want the one that was stored", held.Routes)
	}
	if route := held.Routes[0]; route.Path != "/api" || route.Service != "web" {
		t.Fatalf("route = %+v, want it read back as it was written", route)
	}

	if rec := doAs(t, a, a.secret, http.MethodDelete,
		"/v1/balancers/"+created.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(t, a, a.secret, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"edge","target_port":80,"listen_port":8443,
			"routes":[{"host":"app.test","service":"web"}]}`)); rec.Code !=
		http.StatusCreated {
		t.Fatalf("recreate: %d %s: the routes of a deleted balancer outlived it and the "+
			"primary key refused the new ones", rec.Code, rec.Body.String())
	}
}

func TestANodeReportsHealthForARouteBackend(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	ids := runningService(t, a, nodeID, "web", 1)

	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":8443,"check":"http","routes":[
			{"host":"app.test","service":"web"}]}`)

	rec := do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/balancers/health",
		strings.NewReader(`{"checks":[{"balancer_id":"`+created.ID+
			`","instance_id":"`+ids[0]+`","healthy":true}]}`))
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	for _, b := range nodeBalancers(t, a, nodeID) {
		if b.ID != created.ID {
			continue
		}
		route := routeOf(t, b, "app.test", "")
		if len(route.Backends) != 1 || route.Backends[0].Probe != "passing" {
			t.Fatalf("route backends = %+v, want the probe report kept: a report dropped as "+
				"'not a member' leaves a checked backend permanently unknown, silently",
				route.Backends)
		}
	}
}

func setRoutes(t *testing.T, a *testApp, secret, id, routes string) (int, balancerBody) {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodPut, "/v1/balancers/"+id+"/routes",
		strings.NewReader(`{"routes":`+routes+`}`))

	var updated balancerBody
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	return rec.Code, updated
}

func TestRoutesCanBeReplacedWithoutLosingThePort(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)
	runningService(t, a, nodeID, "api", 1)

	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[{"host":"app.test","service":"web"}]}`)

	code, updated := setRoutes(t, a, a.secret, created.ID,
		`[{"host":"app.test","service":"web"},{"host":"api.test","service":"api"}]`)
	if code != http.StatusOK {
		t.Fatalf("set: %d", code)
	}
	if len(updated.Routes) != 2 {
		t.Fatalf("routes = %+v, want both", updated.Routes)
	}

	code, shrunk := setRoutes(t, a, a.secret, created.ID,
		`[{"host":"api.test","service":"api"}]`)
	if code != http.StatusOK {
		t.Fatalf("shrink: %d", code)
	}
	if len(shrunk.Routes) != 1 || shrunk.Routes[0].Host != "api.test" {
		t.Fatalf("routes = %+v, want the whole table replaced rather than merged: merging "+
			"leaves no way to remove one route without inventing a delete route",
			shrunk.Routes)
	}

	for _, b := range nodeBalancers(t, a, nodeID) {
		if b.ID != created.ID {
			continue
		}
		if len(b.Routes) != 1 {
			t.Fatalf("the node still sees %+v", b.Routes)
		}
		if b.ListenPort != 8443 {
			t.Fatalf("listen port = %d, want it kept: changing a route must not cost the "+
				"port claim or the certificate", b.ListenPort)
		}
		return
	}
	t.Fatal("the balancer left the node view")
}

func TestReplacingRoutesKeepsEveryRule(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[{"host":"app.test","service":"web"}]}`)

	for _, one := range []struct{ name, routes, want string }{
		{"nothing at all", `[]`, "404"},
		{"a service that does not exist", `[{"host":"a.test","service":"nope"}]`, "404"},
		{"two answers for one request", `[{"host":"a.test","service":"web"},
			{"host":"A.TEST","service":"web"}]`, "two answers"},
		{"a path that is not a path", `[{"host":"a.test","path":"api","service":"web"}]`,
			"starts with a slash"},
	} {
		code, _ := setRoutes(t, a, a.secret, created.ID, one.routes)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d: the rules that hold at create hold here too, "+
				"or this route is the way round every one of them", one.name, code,
				http.StatusBadRequest)
		}
	}

	if _, held := setRoutes(t, a, a.secret, created.ID,
		`[{"host":"app.test","service":"web"}]`); len(held.Routes) != 1 {
		t.Fatal("a refused replacement left the balancer without its routes")
	}
}

func TestABalancerWithoutRoutesCannotGrowThem(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	created := createBalancer(t, a, a.secret, `{"name":"plain","target_port":80,
		"listen_port":8444,"service":"web"}`)

	code, _ := setRoutes(t, a, a.secret, created.ID,
		`[{"host":"app.test","service":"web"}]`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a plain balancer is an nftables rule and a routing "+
			"one is a userspace listener, so this would move the port from the kernel to "+
			"the agent behind the operator's back", code, http.StatusBadRequest)
	}
}

func TestAnotherProjectCannotReplaceRoutes(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[{"host":"app.test","service":"web"}]}`)

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	if code, _ := setRoutes(t, a, outsider, created.ID,
		`[{"host":"theirs.test","service":"web"}]`); code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", code, http.StatusNotFound)
	}
}

func TestAMemberCanReplaceRoutes(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningService(t, a, nodeID, "web", 1)

	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":8443,"routes":[{"host":"app.test","service":"web"}]}`)

	member := memberToken(t, a)
	if code, _ := setRoutes(t, a, member, created.ID,
		`[{"host":"other.test","service":"web"}]`); code != http.StatusOK {
		t.Fatalf("status = %d, want %d: a new route is invisible to a member until it is in "+
			"memberPaths, and the failure is a 403 at runtime rather than at compile time",
			code, http.StatusOK)
	}
}
