package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type fakeEvents struct {
	entries []Event
}

func (f fakeEvents) Since(_ context.Context, afterID int64, limit int) ([]Event, error) {
	out := make([]Event, 0, limit)
	for _, entry := range f.entries {
		if entry.ID > afterID && len(out) < limit {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (f fakeEvents) NewestID(context.Context) (int64, error) {
	var newest int64
	for _, entry := range f.entries {
		if entry.ID > newest {
			newest = entry.ID
		}
	}
	return newest, nil
}

type counter struct {
	mu     sync.Mutex
	hits   int
	status int
}

func (c *counter) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		c.mu.Lock()
		c.hits++
		status := c.status
		c.mu.Unlock()

		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	})
}

func (c *counter) answer(status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
}

func (c *counter) seen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

func newTestModule(t *testing.T, events Events) (*Module, *time.Time) {
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
	m.UseEvents(events)
	m.AllowLoopbackBecauseThisIsATest()

	clock := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	m.svc.now = func() time.Time { return clock }
	return m, &clock
}

func subscribe(t *testing.T, m *Module, url string) Subscription {
	t.Helper()

	sub, err := m.svc.create(context.Background(), CreateParams{
		ProjectID: "prj-default", Name: "ops", URL: url,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return sub
}

func onlyDelivery(t *testing.T, m *Module, id string) Delivery {
	t.Helper()

	found, err := m.svc.deliveries(context.Background(), "prj-default", id, 10)
	if err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("deliveries = %d, want one", len(found))
	}
	return found[0]
}

func oneEvent() fakeEvents {
	return fakeEvents{entries: []Event{{
		ID: 1, ProjectID: "prj-default", Kind: "instance.failed", Subject: "i-1",
	}}}
}

func TestARetryWaitsForItsBackoffThenSucceeds(t *testing.T) {
	box := &counter{status: http.StatusBadGateway}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	m, clock := newTestModule(t, oneEvent())
	sub := subscribe(t, m, target.URL+"/hook")
	ctx := context.Background()

	m.svc.repo.setCursor(ctx, 0)
	m.Pump(ctx)

	first := onlyDelivery(t, m, sub.ID)
	if first.State != StatePending || first.Attempts != 1 {
		t.Fatalf("delivery = %+v, want one failed attempt still pending", first)
	}
	if !first.NextAttemptAt.After(*clock) {
		t.Fatal("the retry was scheduled for now, so a broken target would be hammered")
	}

	box.answer(http.StatusOK)
	m.Deliver(ctx)

	if onlyDelivery(t, m, sub.ID).Attempts != 1 {
		t.Fatal("it retried before the backoff elapsed")
	}

	*clock = clock.Add(2 * FirstBackoff)
	m.Deliver(ctx)

	settled := onlyDelivery(t, m, sub.ID)
	if settled.State != StateDelivered {
		t.Fatalf("delivery = %+v, want it delivered once the backoff passed", settled)
	}
	if settled.LastError != "" {
		t.Fatalf("last_error = %q, want it cleared on success", settled.LastError)
	}
}

func TestADeadTargetIsGivenUpOnRatherThanRetriedForever(t *testing.T) {
	box := &counter{status: http.StatusInternalServerError}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	m, clock := newTestModule(t, oneEvent())
	sub := subscribe(t, m, target.URL+"/hook")
	ctx := context.Background()

	m.svc.repo.setCursor(ctx, 0)
	m.Pump(ctx)

	for range MaxAttempts + 3 {
		*clock = clock.Add(2 * MaxBackoff)
		m.Deliver(ctx)
	}

	settled := onlyDelivery(t, m, sub.ID)
	if settled.State != StateFailed {
		t.Fatalf("delivery = %+v, want it dead-lettered", settled)
	}
	if settled.Attempts != MaxAttempts {
		t.Fatalf("attempts = %d, want it to stop at %d", settled.Attempts, MaxAttempts)
	}
	if box.seen() != MaxAttempts {
		t.Fatalf("the target was hit %d times, want %d", box.seen(), MaxAttempts)
	}
}

func TestTheCursorMeansAnEventIsFannedOutOnce(t *testing.T) {
	box := &counter{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	m, _ := newTestModule(t, oneEvent())
	sub := subscribe(t, m, target.URL+"/hook")
	ctx := context.Background()

	m.svc.repo.setCursor(ctx, 0)
	for range 5 {
		m.Pump(ctx)
	}

	found, err := m.svc.deliveries(ctx, "prj-default", sub.ID, 10)
	if err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("deliveries = %d, want the event queued once across five passes", len(found))
	}
	if box.seen() != 1 {
		t.Fatalf("the target was hit %d times, want once", box.seen())
	}
}

func TestADeletedWebhookTakesItsDeliveriesWithIt(t *testing.T) {
	box := &counter{status: http.StatusInternalServerError}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	m, _ := newTestModule(t, oneEvent())
	sub := subscribe(t, m, target.URL+"/hook")
	ctx := context.Background()

	m.svc.repo.setCursor(ctx, 0)
	m.Pump(ctx)

	if err := m.svc.remove(ctx, "prj-default", sub.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	due, err := m.svc.repo.due(ctx, time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due = %d, want a deleted webhook to leave no queue behind", len(due))
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	var reached bool
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer secret.Close()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, secret.URL+"/elsewhere", http.StatusFound)
	}))
	defer target.Close()

	m, _ := newTestModule(t, oneEvent())
	sub := subscribe(t, m, target.URL+"/hook")
	ctx := context.Background()

	m.svc.repo.setCursor(ctx, 0)
	m.Pump(ctx)

	if reached {
		t.Fatal("the redirect was followed, which is how a guard gets walked around")
	}
	if got := onlyDelivery(t, m, sub.ID); got.State == StateDelivered {
		t.Fatalf("delivery = %+v, want a redirect treated as a failure", got)
	}
}
