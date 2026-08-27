package balancer

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type fakeMembers struct {
	members map[string]Member
}

func (f fakeMembers) Member(_ context.Context, instanceID string) (Member, error) {
	member, known := f.members[instanceID]
	if !known {
		return Member{}, fault.NotFound("instance_not_found", "no such instance")
	}
	return member, nil
}

func running() fakeMembers {
	return fakeMembers{members: map[string]Member{
		"i-1": {ProjectID: "prj-default", NodeID: "n-1", Address: "10.20.0.65", Running: true},
		"i-2": {ProjectID: "prj-default", NodeID: "n-2", Address: "10.20.0.129", Running: true},
	}}
}

func newTestModule(t *testing.T) (*Module, *time.Time) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard))
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	m.UseMembers(running())

	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	m.svc.now = func() time.Time { return clock }
	return m, &clock
}

func checked(t *testing.T, m *Module, projectID, name string) Balancer {
	t.Helper()

	b, err := m.svc.get(context.Background(), projectID, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func TestAReportGoesStaleAfterTheGrace(t *testing.T) {
	m, clock := newTestModule(t)
	ctx := context.Background()

	created, err := m.svc.create(ctx, CreateParams{
		ProjectID: "prj-default", Name: "web", TargetPort: 80, ListenPort: 8080,
		Check: CheckTCP, Instances: []string{"i-1"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := m.svc.reportHealth(ctx, "n-1", []Report{
		{BalancerID: created.ID, InstanceID: "i-1", Healthy: true},
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	b := checked(t, m, "prj-default", "web")
	if !b.Backends[0].Healthy {
		t.Fatalf("backend = %+v, want it up right after a passing report", b.Backends[0])
	}

	*clock = clock.Add(HealthGrace - time.Second)
	if b := checked(t, m, "prj-default", "web"); !b.Backends[0].Healthy {
		t.Fatal("the report went stale one second before the grace expired")
	}

	*clock = clock.Add(2 * time.Second)
	b = checked(t, m, "prj-default", "web")
	if b.Backends[0].Healthy {
		t.Fatal("a report older than the grace still counts as healthy")
	}
	if b.Backends[0].Probe != ProbeUnknown {
		t.Fatalf("probe = %q, want unknown once the report is stale", b.Backends[0].Probe)
	}
	if b.Backends[0].Reason == "" {
		t.Fatal("a stale backend gives no reason, so nobody could tell why it went down")
	}
}

func TestStalenessDoesNotTouchAnUncheckedBalancer(t *testing.T) {
	m, clock := newTestModule(t)
	ctx := context.Background()

	if _, err := m.svc.create(ctx, CreateParams{
		ProjectID: "prj-default", Name: "web", TargetPort: 80, ListenPort: 8080,
		Instances: []string{"i-1"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	*clock = clock.Add(100 * HealthGrace)

	b := checked(t, m, "prj-default", "web")
	if !b.Backends[0].Healthy {
		t.Fatal("a balancer with no check went down through staleness it never opted into")
	}
	if b.Backends[0].Probe != "" {
		t.Fatalf("probe = %q, want it empty when no check is configured", b.Backends[0].Probe)
	}
}

func TestAReportOnlyCountsFromTheNodeHoldingTheInstance(t *testing.T) {
	m, _ := newTestModule(t)
	ctx := context.Background()

	created, err := m.svc.create(ctx, CreateParams{
		ProjectID: "prj-default", Name: "web", TargetPort: 80, ListenPort: 8080,
		Check: CheckTCP, Instances: []string{"i-1", "i-2"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := m.svc.reportHealth(ctx, "n-1", []Report{
		{BalancerID: created.ID, InstanceID: "i-1", Healthy: true},
		{BalancerID: created.ID, InstanceID: "i-2", Healthy: true},
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	b := checked(t, m, "prj-default", "web")
	byID := map[string]Backend{}
	for _, backend := range b.Backends {
		byID[backend.InstanceID] = backend
	}

	if !byID["i-1"].Healthy {
		t.Fatal("n-1 could not vouch for the instance it holds")
	}
	if byID["i-2"].Healthy {
		t.Fatal("n-1 vouched for an instance held by n-2 and was believed")
	}
}

func TestAFailingReportIsRememberedWithItsReason(t *testing.T) {
	m, _ := newTestModule(t)
	ctx := context.Background()

	created, err := m.svc.create(ctx, CreateParams{
		ProjectID: "prj-default", Name: "web", TargetPort: 80, ListenPort: 8080,
		Check: CheckHTTP, CheckPath: "/healthz", Instances: []string{"i-1"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := m.svc.reportHealth(ctx, "n-1", []Report{
		{BalancerID: created.ID, InstanceID: "i-1", Healthy: false, Reason: "http status 503"},
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	backend := checked(t, m, "prj-default", "web").Backends[0]
	if backend.Healthy {
		t.Fatal("a failing backend is healthy")
	}
	if !backend.Running {
		t.Fatal("running went false, but the instance is still running")
	}
	if backend.Probe != ProbeFailing || backend.Reason != "http status 503" {
		t.Fatalf("backend = %+v, want the reason kept", backend)
	}
}

func TestAnOverlongReasonIsTruncatedRatherThanStored(t *testing.T) {
	m, _ := newTestModule(t)
	ctx := context.Background()

	created, err := m.svc.create(ctx, CreateParams{
		ProjectID: "prj-default", Name: "web", TargetPort: 80, ListenPort: 8080,
		Check: CheckTCP, Instances: []string{"i-1"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	long := make([]byte, 4096)
	for i := range long {
		long[i] = 'x'
	}

	if err := m.svc.reportHealth(ctx, "n-1", []Report{
		{BalancerID: created.ID, InstanceID: "i-1", Healthy: false, Reason: string(long)},
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	backend := checked(t, m, "prj-default", "web").Backends[0]
	if len(backend.Reason) != MaxPathLength {
		t.Fatalf("reason is %d characters, want it capped at %d",
			len(backend.Reason), MaxPathLength)
	}
}
