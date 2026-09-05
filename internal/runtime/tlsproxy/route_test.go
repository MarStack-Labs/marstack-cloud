package tlsproxy

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func httpBackend(t *testing.T, name string) (string, int) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Backend", name)
			io.WriteString(w, name+" "+r.Host+" "+r.URL.Path+" "+
				r.Header.Get("X-Forwarded-For"))
		}))
	t.Cleanup(server.Close)

	host, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	number, _ := strconv.Atoi(port)
	return host, number
}

func httpBackendOn(t *testing.T, host string, port int, name string) int {
	t.Helper()

	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Skipf("cannot listen on %s:%d here: %v", host, port, err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, name+" "+r.Host+" "+r.URL.Path)
		}))
	server.Listener = l
	server.Start()
	t.Cleanup(server.Close)

	_, bound, _ := net.SplitHostPort(l.Addr().String())
	number, _ := strconv.Atoi(bound)
	return number
}

func routing(t *testing.T, e Endpoint) *Manager {
	t.Helper()

	m := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(m.Close)

	if err := m.Apply(context.Background(), []Endpoint{e}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return m
}

func ask(t *testing.T, port int, host, path string) (int, string) {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet,
		"http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+path, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Host = host

	client := &http.Client{Timeout: 3 * time.Second}
	answer, err := client.Do(request)
	if err != nil {
		t.Fatalf("ask %s%s: %v", host, path, err)
	}
	defer answer.Body.Close()

	body, _ := io.ReadAll(answer.Body)
	return answer.StatusCode, strings.TrimSpace(string(body))
}

func TestTheMostSpecificRouteWins(t *testing.T) {
	table := compileRoutes([]EndpointRoute{
		{Path: "/", Targets: []string{"catch-all"}},
		{Host: "app.test", Targets: []string{"app"}},
		{Host: "app.test", Path: "/api", Targets: []string{"api"}},
		{Host: "app.test", Path: "/api/v2", Targets: []string{"api-v2"}},
		{Path: "/status", Targets: []string{"status"}},
	})

	for _, one := range []struct {
		host, path, want string
	}{
		{"app.test", "/", "app"},
		{"app.test", "/index.html", "app"},
		{"app.test", "/api", "api"},
		{"app.test", "/api/users", "api"},
		{"app.test", "/api/v2/users", "api-v2"},
		{"app.test", "/status", "app"},
		{"other.test", "/status", "status"},
		{"other.test", "/anything", "catch-all"},
	} {
		route, matched := table.match(one.host, one.path)
		if !matched {
			t.Errorf("%s%s matched nothing", one.host, one.path)
			continue
		}
		if route.targets[0] != one.want {
			t.Errorf("%s%s went to %s, want %s: an exact host beats any host, and the "+
				"longest path beats a shorter one",
				one.host, one.path, route.targets[0], one.want)
		}
	}
}

func TestAHostWithAPortStillMatches(t *testing.T) {
	table := compileRoutes([]EndpointRoute{{Host: "app.test", Targets: []string{"app"}}})

	for _, host := range []string{"app.test", "APP.TEST", "app.test:8443", " app.test "} {
		if _, matched := table.match(host, "/"); !matched {
			t.Errorf("Host %q matched nothing, so a client that names the port it dialled "+
				"gets a 404", host)
		}
	}
}

func TestAPathPrefixOnlyMatchesWholeSegments(t *testing.T) {
	table := compileRoutes([]EndpointRoute{{Path: "/api", Targets: []string{"api"}}})

	if _, matched := table.match("app.test", "/apiary"); matched {
		t.Error("/api matched /apiary: a prefix that ignores the segment boundary sends " +
			"one application's traffic to another")
	}
	for _, path := range []string{"/api", "/api/", "/api/users"} {
		if _, matched := table.match("app.test", path); !matched {
			t.Errorf("/api did not match %s", path)
		}
	}
}

func TestARequestThatMatchesNothingIsRefused(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	port := freePort(t)

	routing(t, Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: targetPort,
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: []string{address}},
		},
	})

	code, _ := ask(t, port, "somewhere.else", "/")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: a request nothing claims must be answered, not "+
			"handed to whichever backend happened to be first", code, http.StatusNotFound)
	}
}

func TestARouteWithNoBackendUpSaysSo(t *testing.T) {
	_, targetPort := httpBackend(t, "web")
	port := freePort(t)

	routing(t, Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: targetPort,
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: nil},
		},
	})

	code, _ := ask(t, port, "app.test", "/")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: a route whose service holds nothing is a different "+
			"thing from a route that does not exist", code, http.StatusServiceUnavailable)
	}
}

func TestTheBackendSeesTheHostTheClientAsked(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	port := freePort(t)

	routing(t, Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: targetPort,
		Routes: []EndpointRoute{
			{Host: "app.test", Path: "/api", Targets: []string{address}},
		},
	})

	code, body := ask(t, port, "app.test", "/api/users")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if !strings.Contains(body, "app.test") {
		t.Errorf("the backend was asked for %q, want the host the client used: a backend "+
			"that serves several names cannot tell which one was wanted", body)
	}
	if !strings.Contains(body, "/api/users") {
		t.Errorf("the backend was asked for %q, want the whole path: trimming the prefix "+
			"would make every route rewrite what it forwards", body)
	}
	if !strings.Contains(body, "127.0.0.1") {
		t.Errorf("X-Forwarded-For is missing from %q, so the backend sees the node as the "+
			"client and no log downstream knows who called", body)
	}
}

func TestARoutingListenerCanTerminateTLS(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	certPEM, keyPEM := selfSigned(t)
	port := freePort(t)

	routing(t, Endpoint{
		ID:          "lb-1",
		ListenPort:  port,
		TargetPort:  targetPort,
		Certificate: certPEM,
		PrivateKey:  keyPEM,
		Routes: []EndpointRoute{
			{Host: "balancer.test", Targets: []string{address}},
		},
	})

	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	request, err := http.NewRequest(http.MethodGet,
		"https://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+"/", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Host = "balancer.test"

	answer, err := client.Do(request)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer answer.Body.Close()

	body, _ := io.ReadAll(answer.Body)
	if answer.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", answer.StatusCode, body)
	}
	if !strings.Contains(string(body), "balancer.test") {
		t.Fatalf("body = %q, want the request routed by the host header inside the tunnel", body)
	}
}

func TestEachRouteKeepsItsOwnBackends(t *testing.T) {
	shared := freePort(t)
	httpBackendOn(t, "127.0.0.1", shared, "web")
	httpBackendOn(t, "127.0.0.2", shared, "api")

	port := freePort(t)
	routing(t, Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: shared,
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: []string{"127.0.0.1"}},
			{Host: "api.test", Targets: []string{"127.0.0.2"}},
		},
	})

	for host, want := range map[string]string{"app.test": "web", "api.test": "api"} {
		code, body := ask(t, port, host, "/")
		if code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", host, code, body)
		}
		if !strings.HasPrefix(body, want) {
			t.Errorf("%s was answered by %q, want %s", host, body, want)
		}
	}
}

func TestARouteSpreadsRequestsOverItsBackends(t *testing.T) {
	table := compileRoutes([]EndpointRoute{
		{Host: "app.test", Targets: []string{"10.0.0.1", "10.0.0.2"}},
	})

	route, matched := table.match("app.test", "/")
	if !matched {
		t.Fatal("the route matched nothing")
	}

	seen := map[string]int{}
	for range 4 {
		target, up := route.pick()
		if !up {
			t.Fatal("a route with two targets picked nothing")
		}
		seen[target]++
	}

	if seen["10.0.0.1"] != 2 || seen["10.0.0.2"] != 2 {
		t.Fatalf("picks = %v, want each backend twice: a route that always answers with the "+
			"first target is not balancing anything", seen)
	}
}

func TestABackendIsChosenPerRequest(t *testing.T) {
	shared := freePort(t)
	httpBackendOn(t, "127.0.0.1", shared, "one")
	httpBackendOn(t, "127.0.0.2", shared, "two")

	port := freePort(t)
	routing(t, Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: shared,
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: []string{"127.0.0.1", "127.0.0.2"}},
		},
	})

	seen := map[string]bool{}
	for range 6 {
		_, body := ask(t, port, "app.test", "/")
		seen[strings.Fields(body)[0]] = true
	}

	if len(seen) < 2 {
		t.Fatalf("six requests reached %v, want both backends: a route that always picks "+
			"the first target is not balancing anything", seen)
	}
}

func TestAMembershipChangeKeepsTheListenerUp(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	port := freePort(t)

	e := Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: targetPort,
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: nil},
		},
	}
	m := routing(t, e)
	before := m.fingerprintOf("lb-1")

	e.Routes[0].Targets = []string{address}
	if err := m.Apply(context.Background(), []Endpoint{e}); err != nil {
		t.Fatalf("apply again: %v", err)
	}

	if after := m.fingerprintOf("lb-1"); after != before {
		t.Fatal("the listener was restarted because a replica came or went, which drops " +
			"every live connection at exactly the moment connections matter")
	}
	if code, body := ask(t, port, "app.test", "/"); code != http.StatusOK {
		t.Fatalf("status = %d: %s, want the new backend used without a restart", code, body)
	}
}

func TestAddingARouteRestartsTheListener(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	port := freePort(t)

	plain := Endpoint{
		ID:          "lb-1",
		ListenPort:  port,
		TargetPort:  targetPort,
		Certificate: "",
		PrivateKey:  "",
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: []string{address}},
		},
	}
	m := routing(t, plain)
	before := m.fingerprintOf("lb-1")

	plain.Routes = append(plain.Routes,
		EndpointRoute{Host: "api.test", Targets: []string{address}})
	if err := m.Apply(context.Background(), []Endpoint{plain}); err != nil {
		t.Fatalf("apply again: %v", err)
	}

	if m.fingerprintOf("lb-1") != before {
		t.Fatal("adding a route restarted the listener, which is not needed: the table is " +
			"swapped behind an atomic pointer")
	}
	if code, _ := ask(t, port, "api.test", "/"); code != http.StatusOK {
		t.Fatalf("status = %d, want the route that was just added to answer", code)
	}
}

func TestALisenerWithNeitherCertificateNorRoutesIsRefused(t *testing.T) {
	m := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(m.Close)

	err := m.Apply(context.Background(), []Endpoint{{
		ID:         "lb-1",
		ListenPort: freePort(t),
		TargetPort: 80,
		Targets:    []string{"127.0.0.1"},
	}})

	if err == nil {
		t.Fatal("a plain listener with nothing to do was started, so a balancer that should " +
			"be an nftables rule is answered in userspace instead")
	}
}

func TestGivingABalancerRoutesRestartsTheListener(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	certPEM, keyPEM := selfSigned(t)
	port := freePort(t)

	spliced := Endpoint{
		ID:          "lb-1",
		ListenPort:  port,
		TargetPort:  targetPort,
		Certificate: certPEM,
		PrivateKey:  keyPEM,
		Targets:     []string{address},
	}
	m := routing(t, spliced)
	before := m.fingerprintOf("lb-1")

	routed := spliced
	routed.Routes = []EndpointRoute{{Host: "app.test", Targets: []string{address}}}
	if err := m.Apply(context.Background(), []Endpoint{routed}); err != nil {
		t.Fatalf("apply again: %v", err)
	}

	if m.fingerprintOf("lb-1") == before {
		t.Fatal("the listener kept splicing raw bytes after the balancer was given routes, " +
			"so every request goes to a round robin backend and no host is ever read")
	}
}

func TestTheChallengeIsAnsweredBeforeAnyRoute(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	port := freePort(t)

	m := routing(t, Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: targetPort,
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: []string{address}},
		},
	})
	m.UseChallenges(map[string]string{"tok3n": "tok3n.thumbprint"})

	code, body := ask(t, port, "app.test", "/.well-known/acme-challenge/tok3n")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d: the authority fetches this over plain http on the "+
			"listen port, and a route sending it to a backend answers with whatever that "+
			"backend says", code, http.StatusOK)
	}
	if body != "tok3n.thumbprint" {
		t.Fatalf("body = %q, want the key authorization exactly. The authority compares it "+
			"byte for byte", body)
	}
}

func TestAnUnknownChallengeTokenIsNotAnswered(t *testing.T) {
	address, targetPort := httpBackend(t, "web")
	port := freePort(t)

	m := routing(t, Endpoint{
		ID:         "lb-1",
		ListenPort: port,
		TargetPort: targetPort,
		Routes: []EndpointRoute{
			{Host: "app.test", Targets: []string{address}},
		},
	})
	m.UseChallenges(map[string]string{"mine": "mine.thumbprint"})

	code, _ := ask(t, port, "app.test", "/.well-known/acme-challenge/somebody-elses")
	if code == http.StatusOK {
		t.Fatal("a token this node was never given was answered, which would let anybody " +
			"who can guess a path prove they own the name")
	}
}
