package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func probingAgent(t *testing.T) *Agent {
	t.Helper()
	return New(Config{Endpoint: "http://127.0.0.1:1", Name: "bm-1", Interval: time.Hour},
		Deps{}, logging.New("error", io.Discard))
}

func splitHostPort(t *testing.T, address string) (string, int) {
	t.Helper()

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split %q: %v", address, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	return host, number
}

func TestATCPProbeReachesAListenerAndNoticesWhenItStops(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	host, port := splitHostPort(t, listener.Addr().String())
	target := probeTarget{address: host, port: port, kind: checkKindTCP}

	if err := probe(context.Background(), target); err != nil {
		t.Fatalf("probe of a live listener failed: %v", err)
	}

	listener.Close()
	if err := probe(context.Background(), target); err == nil {
		t.Fatal("probe of a closed port succeeded")
	}
}

func TestAnHTTPProbeJudgesByStatus(t *testing.T) {
	status := http.StatusOK
	var asked string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.WriteHeader(status)
	}))
	defer server.Close()

	host, port := splitHostPort(t, strings.TrimPrefix(server.URL, "http://"))
	target := probeTarget{address: host, port: port, kind: checkKindHTTP, path: "/healthz"}

	if err := probe(context.Background(), target); err != nil {
		t.Fatalf("a 200 was treated as a failure: %v", err)
	}
	if asked != "/healthz" {
		t.Fatalf("the probe asked for %q, want the configured path", asked)
	}

	status = http.StatusServiceUnavailable
	err := probe(context.Background(), target)
	if err == nil {
		t.Fatal("a 503 was treated as healthy")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("reason = %q, want it to name the status", err.Error())
	}
}

func TestAnHTTPProbeDoesNotFollowARedirectIntoSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1:1/gone")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	host, port := splitHostPort(t, strings.TrimPrefix(server.URL, "http://"))
	target := probeTarget{address: host, port: port, kind: checkKindHTTP, path: "/"}

	if err := probe(context.Background(), target); err != nil {
		t.Fatalf("a 302 is inside 200-399 and should pass without being followed: %v", err)
	}
}

func TestABackendNeedsRiseConsecutivePassesToComeUp(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	host, port := splitHostPort(t, listener.Addr().String())
	a := probingAgent(t)
	target := probeTarget{
		balancerID: "lb-1", instanceID: "i-1",
		address: host, port: port, kind: checkKindTCP, rise: 3, fall: 2,
	}

	for pass := 1; pass <= 2; pass++ {
		verdict, reason, _ := a.settle(context.Background(), target)
		if verdict {
			t.Fatalf("pass %d: up after %d of 3 passes", pass, pass)
		}
		if !strings.Contains(reason, "needed to come up") {
			t.Fatalf("pass %d: reason = %q, want it to say how far along it is", pass, reason)
		}
	}

	verdict, reason, changed := a.settle(context.Background(), target)
	if !verdict {
		t.Fatal("still down after three consecutive passes")
	}
	if reason != "" {
		t.Fatalf("reason = %q, want nothing said about a healthy backend", reason)
	}
	if !changed {
		t.Fatal("coming up was not reported as a change")
	}
}

func TestABackendStaysUpUntilFallConsecutiveFailures(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	host, port := splitHostPort(t, listener.Addr().String())
	a := probingAgent(t)
	target := probeTarget{
		balancerID: "lb-1", instanceID: "i-1",
		address: host, port: port, kind: checkKindTCP, rise: 1, fall: 3,
	}

	if verdict, _, _ := a.settle(context.Background(), target); !verdict {
		t.Fatal("did not come up with rise 1")
	}

	listener.Close()

	for failure := 1; failure <= 2; failure++ {
		verdict, reason, changed := a.settle(context.Background(), target)
		if !verdict {
			t.Fatalf("failure %d: dropped before reaching fall 3", failure)
		}
		if changed {
			t.Fatalf("failure %d: reported a change while the verdict held", failure)
		}
		if !strings.Contains(reason, "still up") {
			t.Fatalf("failure %d: reason = %q, want it to say it is wobbling", failure, reason)
		}
	}

	verdict, reason, changed := a.settle(context.Background(), target)
	if verdict {
		t.Fatal("still up after three consecutive failures")
	}
	if !changed {
		t.Fatal("going down was not reported as a change")
	}
	if reason == "" {
		t.Fatal("went down without saying why")
	}
}

func TestOneGoodProbeResetsTheFailureRun(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	host, port := splitHostPort(t, listener.Addr().String())
	a := probingAgent(t)

	live := probeTarget{
		balancerID: "lb-1", instanceID: "i-1",
		address: host, port: port, kind: checkKindTCP, rise: 1, fall: 3,
	}
	dead := live
	dead.port = 1

	a.settle(context.Background(), live)
	a.settle(context.Background(), dead)
	a.settle(context.Background(), dead)
	a.settle(context.Background(), live)

	if verdict, _, _ := a.settle(context.Background(), dead); !verdict {
		t.Fatal("the failure run was not reset by the passing probe in the middle")
	}
}

func TestProbeTargetsOnlyCoversCheckedBalancersOnThisNode(t *testing.T) {
	balancers := []balancerView{
		{
			ID: "lb-none", TargetPort: 80, Check: "none",
			Backends: []balancerBackendView{{InstanceID: "i-1", Address: "10.20.0.65"}},
		},
		{
			ID: "lb-empty", TargetPort: 80,
			Backends: []balancerBackendView{{InstanceID: "i-1", Address: "10.20.0.65"}},
		},
		{
			ID: "lb-tcp", TargetPort: 8080, Check: "tcp", Rise: 2, Fall: 2,
			Backends: []balancerBackendView{
				{InstanceID: "i-1", Address: "10.20.0.65"},
				{InstanceID: "i-elsewhere", Address: "10.20.0.130"},
				{InstanceID: "i-2", Address: ""},
			},
		},
	}

	targets := probeTargets(balancers, map[string]bool{"i-1": true, "i-2": true})

	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want only the checked backend this node holds", targets)
	}
	got := targets[0]
	if got.balancerID != "lb-tcp" || got.instanceID != "i-1" || got.port != 8080 {
		t.Fatalf("target = %+v", got)
	}
	if got.rise != 2 || got.fall != 2 {
		t.Fatalf("target = %+v, want the thresholds carried through", got)
	}
}

func TestProbeStateIsForgottenWhenABackendLeaves(t *testing.T) {
	a := probingAgent(t)

	a.probes["lb-1/i-1"] = &probeState{settled: true, verdict: true}
	a.probes["lb-1/i-2"] = &probeState{settled: true, verdict: true}

	a.forgetProbes(map[string]bool{"lb-1/i-1": true})

	if _, still := a.probes["lb-1/i-2"]; still {
		t.Fatal("the state of a removed backend survived, so re-adding it would inherit a verdict")
	}
	if _, kept := a.probes["lb-1/i-1"]; !kept {
		t.Fatal("the state of a backend that is still there was dropped")
	}
}

func TestProbesReachTheControlPlane(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	host, port := splitHostPort(t, strings.TrimPrefix(backend.URL, "http://"))

	in := runningInstance()
	cp := &controlPlane{
		instances: []instanceView{in},
		networks:  []networkView{defaultNetworkView(in.ID, host)},
		balancers: []balancerView{
			{
				ID: "lb-1", Name: "web", Protocol: "tcp", ListenPort: 9000, TargetPort: port,
				Check: "http", CheckPath: "/healthz", Rise: 1, Fall: 1,
				Backends: []balancerBackendView{{InstanceID: in.ID, Address: host}},
			},
		},
	}

	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtimes: runtimesFor(rt), Datapath: &fakeDatapath{}},
		logging.New("error", io.Discard))

	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	a.reconcile(context.Background())

	reports := cp.healthReports()
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want one for the checked backend", reports)
	}
	if !reports[0].Healthy || reports[0].InstanceID != in.ID {
		t.Fatalf("report = %+v, want the backend reported healthy", reports[0])
	}
}
