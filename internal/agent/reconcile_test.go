package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

type fakeRuntime struct {
	mu       sync.Mutex
	state    workload.State
	present  []string
	started  []workload.Spec
	stopped  []string
	removed  []string
	startErr error
	stopErr  error
	statErr  error
	listErr  error
}

func (f *fakeRuntime) List(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present, f.listErr
}

func (f *fakeRuntime) Name() string { return "fake" }

func (f *fakeRuntime) Start(_ context.Context, spec workload.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, spec)
	f.state = workload.State{Phase: workload.PhaseRunning}
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, instanceID)
	f.state = workload.State{Phase: workload.PhaseExited, Message: "exited with code 0"}
	return nil
}

func (f *fakeRuntime) Status(context.Context, string) (workload.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.statErr != nil {
		return workload.State{}, f.statErr
	}
	return f.state, nil
}

func (f *fakeRuntime) Remove(_ context.Context, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, instanceID)
	return nil
}

func (f *fakeRuntime) removals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

type fakeResolver struct {
	mu       sync.Mutex
	listened []string
	zone     map[string]string
}

func (f *fakeResolver) Listen(_ context.Context, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listened = append(f.listened, address)
	return nil
}

func (f *fakeResolver) Update(records map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zone = records
}

func (f *fakeResolver) snapshotZone() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.zone
}

type fakeDatapath struct {
	mu       sync.Mutex
	routes   []workload.Route
	pruned   []workload.Keep
	pruneErr error
}

func (f *fakeDatapath) Prune(_ context.Context, keep workload.Keep) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.pruneErr != nil {
		return f.pruneErr
	}
	f.pruned = append(f.pruned, keep)
	return nil
}

func (f *fakeDatapath) lastPrune(t *testing.T) workload.Keep {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.pruned) == 0 {
		t.Fatal("the datapath was never pruned")
	}
	return f.pruned[len(f.pruned)-1]
}

func (f *fakeDatapath) ApplyRoutes(_ context.Context, routes []workload.Route) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, routes...)
	return nil
}

func (f *fakeDatapath) snapshot() []workload.Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]workload.Route(nil), f.routes...)
}

type report struct {
	InstanceID string
	State      string
	Message    string
}

type controlPlane struct {
	mu        sync.Mutex
	instances []instanceView
	networks  []networkView
	nodes     []nodeView
	records   []dnsRecordView
	reports   []report
}

func (c *controlPlane) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/nodes/register", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc", Name: "bm-1"})
	})
	mux.HandleFunc("GET /v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		json.NewEncoder(w).Encode(nodeListBody{Nodes: c.nodes})
	})
	mux.HandleFunc("POST /v1/nodes/{id}/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(nodeView{ID: "n-abc"})
	})
	mux.HandleFunc("GET /v1/nodes/{id}/instances", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		json.NewEncoder(w).Encode(instanceListBody{Instances: c.instances})
	})
	mux.HandleFunc("GET /v1/dns/records", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		json.NewEncoder(w).Encode(dnsRecordsBody{Records: c.records})
	})
	mux.HandleFunc("GET /v1/nodes/{id}/network", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		json.NewEncoder(w).Encode(networkListBody{Networks: c.networks})
	})
	mux.HandleFunc("PUT /v1/nodes/{nodeID}/instances/{instanceID}/status",
		func(w http.ResponseWriter, r *http.Request) {
			var body statusBody
			json.NewDecoder(r.Body).Decode(&body)

			c.mu.Lock()
			c.reports = append(c.reports, report{
				InstanceID: r.PathValue("instanceID"),
				State:      body.ObservedState,
				Message:    body.Message,
			})
			c.mu.Unlock()

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		})

	return mux
}

func (c *controlPlane) lastReport(t *testing.T) report {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.reports) == 0 {
		t.Fatal("the agent reported nothing")
	}
	return c.reports[len(c.reports)-1]
}

func newReconcileHarness(t *testing.T, cp *controlPlane, rt workload.Runtime) *Agent {
	t.Helper()

	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtime: rt, Datapath: &fakeDatapath{}}, logging.New("error", io.Discard))

	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	return a
}

func runningInstance() instanceView {
	return instanceView{
		ID:            "i-1",
		Name:          "api-1",
		Image:         "alpine:3.20",
		Command:       []string{"/bin/sh", "-c", "sleep 100"},
		VCPU:          1,
		MemoryMiB:     512,
		DesiredState:  "running",
		ObservedState: "pending",
	}
}

func TestReconcileStartsAWorkloadThatShouldRun(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want 1", len(rt.started))
	}
	if got := rt.started[0]; got.Image != "alpine:3.20" || len(got.Command) != 3 {
		t.Fatalf("spec = %+v, want the image and command passed through", got)
	}
	if got := cp.lastReport(t); got.State != observedRunning {
		t.Fatalf("reported %q, want %q", got.State, observedRunning)
	}
}

func TestReconcileDoesNotRestartAWorkloadAlreadyRunning(t *testing.T) {
	instance := runningInstance()
	instance.ObservedState = "running"

	cp := &controlPlane{
		instances: []instanceView{instance},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 0 {
		t.Fatalf("starts = %d, want 0: the workload is already running", len(rt.started))
	}
	if len(cp.reports) != 0 {
		t.Fatalf("reports = %d, want 0: nothing changed, so nothing to say", len(cp.reports))
	}
}

func TestReconcileStopsAWorkloadThatShouldNotRun(t *testing.T) {
	instance := runningInstance()
	instance.DesiredState = "stopped"
	instance.ObservedState = "running"

	cp := &controlPlane{instances: []instanceView{instance}}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.stopped) != 1 {
		t.Fatalf("stops = %d, want 1", len(rt.stopped))
	}
	if got := cp.lastReport(t); got.State != observedStopped {
		t.Fatalf("reported %q, want %q", got.State, observedStopped)
	}
}

func TestReconcileReportsFailureWhenStartFails(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{
		state:    workload.State{Phase: workload.PhaseAbsent},
		startErr: errors.New("image alpine:3.20 is not present on this node"),
	}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	got := cp.lastReport(t)
	if got.State != observedFailed {
		t.Fatalf("reported %q, want %q", got.State, observedFailed)
	}
	if got.Message == "" {
		t.Fatal("the failure carried no message; an operator cannot act on that")
	}
}

func TestReconcileReportsFailureWhenAWorkloadDies(t *testing.T) {
	instance := runningInstance()
	instance.ObservedState = "running"

	cp := &controlPlane{
		instances: []instanceView{instance},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{state: workload.State{
		Phase:   workload.PhaseExited,
		Message: "exited with code 137",
	}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	got := cp.lastReport(t)
	if got.State != observedFailed {
		t.Fatalf("reported %q, want %q", got.State, observedFailed)
	}
	if got.Message != "exited with code 137" {
		t.Fatalf("message = %q, want the runtime's own explanation", got.Message)
	}
	if len(rt.started) != 0 {
		t.Fatal("the agent restarted a dead workload; restart policy is not implemented yet")
	}
}

func TestReconcileSurvivesAnUninspectableWorkload(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{statErr: errors.New("cgroup vanished")}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if got := cp.lastReport(t); got.State != observedFailed {
		t.Fatalf("reported %q, want %q", got.State, observedFailed)
	}
}

func TestReconcileIsSkippedWithoutARuntime(t *testing.T) {
	cp := &controlPlane{instances: []instanceView{runningInstance()}}

	a := newReconcileHarness(t, cp, nil)
	a.runtime = nil
	a.reconcile(context.Background())

	if len(cp.reports) != 0 {
		t.Fatalf("reports = %d, want 0: an agent with no runtime must not claim anything", len(cp.reports))
	}
}

func defaultNetworkView(instanceID, ip string) networkView {
	return networkView{
		NetworkID: "nw-1",
		Name:      "default",
		Bridge:    "msbr-1",
		CIDR:      "10.20.0.0/16",
		Gateway:   "10.20.0.1",
		Slice:     "10.20.0.64/26",
		NICs:      []nicView{{InstanceID: instanceID, IP: ip, MAC: "02:aa:bb:cc:dd:ee"}},
	}
}

func TestReconcilePassesTheInterfaceIntoTheSpec(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want 1", len(rt.started))
	}

	net := rt.started[0].Network
	if net == nil {
		t.Fatal("the workload was started without an interface")
	}
	if net.IP != "10.20.0.65" || net.Gateway != "10.20.0.1" {
		t.Fatalf("interface = %+v, want the address from the control plane", net)
	}
	if net.Prefix != 26 {
		t.Fatalf("prefix = %d, want the slice prefix: a container that treats the whole "+
			"network as on-link will ARP for addresses that live on another node", net.Prefix)
	}
	if net.BridgeAddr != "10.20.0.1/16" {
		t.Fatalf("bridge address = %q, want the gateway with the network prefix", net.BridgeAddr)
	}
	if net.MAC != "02:aa:bb:cc:dd:ee" {
		t.Fatalf("mac = %q, want the allocated one", net.MAC)
	}
}

func TestReconcileWaitsForAnAddressBeforeStarting(t *testing.T) {
	cp := &controlPlane{instances: []instanceView{runningInstance()}}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 0 {
		t.Fatal("the workload was started before it had an address; it would come up with no network")
	}

	got := cp.lastReport(t)
	if got.State != observedPending {
		t.Fatalf("reported %q, want %q", got.State, observedPending)
	}
	if got.Message == "" {
		t.Fatal("the wait carried no reason, so an operator cannot tell it apart from a stall")
	}
}

func TestReconcileSkipsAnUnusableNetwork(t *testing.T) {
	broken := defaultNetworkView("i-1", "10.20.0.65")
	broken.CIDR = "not-a-cidr"

	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{broken},
	}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 0 {
		t.Fatal("a workload was started from a network the agent could not parse")
	}
	if got := cp.lastReport(t); got.State != observedPending {
		t.Fatalf("reported %q, want %q", got.State, observedPending)
	}
}

func TestReconcileProgramsRoutesToPeerSlices(t *testing.T) {
	network := defaultNetworkView("i-1", "10.20.0.65")
	network.Peers = []peerView{
		{NodeID: "n-two", Slice: "10.20.0.128/26"},
		{NodeID: "n-three", Slice: "10.20.0.192/26"},
	}

	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{network},
		nodes: []nodeView{
			{ID: "n-abc", Address: "192.168.107.5"},
			{ID: "n-two", Address: "192.168.107.6"},
		},
	}

	dp := &fakeDatapath{}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtime: &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}, Datapath: dp},
		logging.New("error", io.Discard))
	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}

	a.reconcile(context.Background())

	routes := dp.snapshot()
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want only the peer with a known address", routes)
	}
	if routes[0].Slice != "10.20.0.128/26" || routes[0].Via != "192.168.107.6" {
		t.Fatalf("route = %+v, want 10.20.0.128/26 via 192.168.107.6", routes[0])
	}
}

func TestReconcileProgramsNoRoutesOnASingleNode(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
		nodes:     []nodeView{{ID: "n-abc", Address: "192.168.107.5"}},
	}

	dp := &fakeDatapath{}
	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtime: &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}, Datapath: dp},
		logging.New("error", io.Discard))
	a.register(context.Background())

	a.reconcile(context.Background())

	if routes := dp.snapshot(); len(routes) != 0 {
		t.Fatalf("routes = %+v, want none: there are no peers", routes)
	}
}

func TestReconcileRemovesWorkloadsTheControlPlaneForgot(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{
		state:   workload.State{Phase: workload.PhaseRunning},
		present: []string{"i-1", "i-gone", "i-also-gone"},
	}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	removed := rt.removals()
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want the two workloads no longer assigned", removed)
	}
	for _, id := range removed {
		if id == "i-1" {
			t.Fatal("a workload that is still assigned was removed")
		}
	}
}

func TestReconcilePrunesDatapathToWhatIsAssigned(t *testing.T) {
	network := defaultNetworkView("i-1", "10.20.0.65")

	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{network},
	}
	dp := &fakeDatapath{}

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtime: &fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}}, Datapath: dp},
		logging.New("error", io.Discard))
	a.register(context.Background())

	a.reconcile(context.Background())

	keep := dp.lastPrune(t)
	if len(keep.Bridges) != 1 || keep.Bridges[0] != network.Bridge {
		t.Fatalf("kept bridges = %v, want only %q", keep.Bridges, network.Bridge)
	}
	if len(keep.Instances) != 1 || keep.Instances[0] != "i-1" {
		t.Fatalf("kept instances = %v, want only i-1", keep.Instances)
	}
}

func TestReconcilePrunesEverythingWhenNothingIsAssigned(t *testing.T) {
	cp := &controlPlane{}
	dp := &fakeDatapath{}
	rt := &fakeRuntime{present: []string{"i-old"}}

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtime: rt, Datapath: dp}, logging.New("error", io.Discard))
	a.register(context.Background())

	a.reconcile(context.Background())

	if removed := rt.removals(); len(removed) != 1 || removed[0] != "i-old" {
		t.Fatalf("removed = %v, want [i-old]", removed)
	}

	keep := dp.lastPrune(t)
	if len(keep.Bridges) != 0 || len(keep.Instances) != 0 {
		t.Fatalf("keep = %+v, want nothing kept", keep)
	}
}

func TestReconcileDoesNotPruneWhenTheControlPlaneIsUnreachable(t *testing.T) {
	dp := &fakeDatapath{}
	rt := &fakeRuntime{present: []string{"i-1"}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			json.NewEncoder(w).Encode(nodeView{ID: "n-abc"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtime: rt, Datapath: dp}, logging.New("error", io.Discard))
	a.register(context.Background())

	a.reconcile(context.Background())

	if removed := rt.removals(); len(removed) != 0 {
		t.Fatalf("removed = %v: a lost control plane must never look like an empty cluster", removed)
	}
	if len(dp.pruned) != 0 {
		t.Fatal("the datapath was pruned from an unknown desired state")
	}
}

func TestReconcileServesTheZoneOnEachGateway(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
		records: []dnsRecordView{
			{FQDN: "web-1.default.internal", IP: "10.20.0.65"},
			{FQDN: "web-2.default.internal", IP: "10.20.0.130"},
		},
	}
	res := &fakeResolver{}

	srv := httptest.NewServer(cp.handler())
	defer srv.Close()

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{
			Runtime:  &fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}},
			Resolver: res,
		}, logging.New("error", io.Discard))
	a.register(context.Background())

	a.reconcile(context.Background())

	if len(res.listened) != 1 || res.listened[0] != "10.20.0.1" {
		t.Fatalf("listened on %v, want the network gateway", res.listened)
	}

	zone := res.snapshotZone()
	if zone["web-2.default.internal"] != "10.20.0.130" {
		t.Fatalf("zone = %v, want a record for a workload on another node", zone)
	}
}

func TestReconcileGivesTheContainerItsResolverAndSearchDomain(t *testing.T) {
	cp := &controlPlane{
		instances: []instanceView{runningInstance()},
		networks:  []networkView{defaultNetworkView("i-1", "10.20.0.65")},
	}
	rt := &fakeRuntime{state: workload.State{Phase: workload.PhaseAbsent}}

	newReconcileHarness(t, cp, rt).reconcile(context.Background())

	if len(rt.started) != 1 {
		t.Fatalf("starts = %d, want 1", len(rt.started))
	}

	net := rt.started[0].Network
	if net.Nameserver != "10.20.0.1" {
		t.Fatalf("nameserver = %q, want the local gateway", net.Nameserver)
	}
	if net.SearchDomain != "default.internal" {
		t.Fatalf("search domain = %q, want default.internal so short names resolve", net.SearchDomain)
	}
}
