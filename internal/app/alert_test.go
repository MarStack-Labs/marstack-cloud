package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

type alertBody struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	InstanceID string  `json:"instance_id"`
	State      string  `json:"state"`
	LastValue  float64 `json:"last_value"`
	Message    string  `json:"message"`
}

func newAlertingApp(t *testing.T) (*testApp, string, *movingClock) {
	t.Helper()

	tick := &movingClock{at: time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)}
	dir := schemaDir(t)

	built, err := New(context.Background(),
		Config{
			DataDir:           dir,
			SchedulerInterval: 10 * time.Millisecond,
			RatePerSecond:     &unlimited,
			Now:               tick.now,
		}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { built.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}

	a := &testApp{App: built, secret: strings.TrimSpace(string(raw))}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.scheduler.Run(ctx)

	return a, registerNode(t, a, "bm-1", "rack-a"), tick
}

func createAlert(t *testing.T, a *testApp, body string) alertBody {
	t.Helper()

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/alerts", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create alert: %d %s", rec.Code, rec.Body.String())
	}

	var created alertBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func readAlert(t *testing.T, a *testApp, ref string) alertBody {
	t.Helper()

	rec := doAs(t, a, a.secret, http.MethodGet, "/v1/alerts/"+ref, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read alert: %d %s", rec.Code, rec.Body.String())
	}

	var held alertBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return held
}

func busyReport(instanceID string, percent int) string {
	return `{"cpu_percent":50,"memory_used_mib":1000,"memory_mib":6144,
		"instances":[{"instance_id":"` + instanceID + `","cpu_percent":` +
		itoaFloat(percent) + `,"memory_used_mib":500}]}`
}

func itoaFloat(value int) string {
	return strings.TrimSpace(strings.Join([]string{"", string(rune('0' + value/10)),
		string(rune('0' + value%10))}, ""))
}

func TestAnAlertFiresOnlyAfterTheLoadHolds(t *testing.T) {
	a, nodeID, tick := newAlertingApp(t)
	id := runningInstance(t, a, a.secret, "web-1", nodeID)

	created := createAlert(t, a, `{"name":"hot","instance_id":"web-1","metric":"cpu",
		"threshold":80,"for":"2m"}`)

	reportUsage(t, a, nodeID, busyReport(id, 95))
	a.alerts.Sweep(context.Background())

	if held := readAlert(t, a, created.ID); held.State != "warming" {
		t.Fatalf("state = %q, want warming: one high reading is a spike", held.State)
	}
	if entries := readEvents(t, a, a.secret,
		"?subject="+id+"&kind=alert.firing"); len(entries) != 0 {
		t.Fatalf("events = %+v, want nothing while it is only warming", entries)
	}

	tick.advance(3 * time.Minute)
	reportUsage(t, a, nodeID, busyReport(id, 92))
	a.alerts.Sweep(context.Background())

	held := readAlert(t, a, created.ID)
	if held.State != "firing" {
		t.Fatalf("state = %q (%s), want firing once it held", held.State, held.Message)
	}

	entries := readEvents(t, a, a.secret, "?subject="+id+"&kind=alert.firing")
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want exactly one", entries)
	}
	if !strings.Contains(entries[0].Message, "hot") {
		t.Fatalf("message = %q, want the alert named", entries[0].Message)
	}
}

func TestAFiringAlertIsAnnouncedOnceAndClearedOnce(t *testing.T) {
	a, nodeID, tick := newAlertingApp(t)
	id := runningInstance(t, a, a.secret, "web-1", nodeID)

	created := createAlert(t, a, `{"name":"hot","instance_id":"web-1","metric":"cpu",
		"threshold":80,"for":"1m"}`)

	for range 4 {
		reportUsage(t, a, nodeID, busyReport(id, 95))
		a.alerts.Sweep(context.Background())
		tick.advance(90 * time.Second)
	}

	firing := readEvents(t, a, a.secret, "?subject="+id+"&kind=alert.firing")
	if len(firing) != 1 {
		t.Fatalf("firing events = %d, want one. A sweep every twenty seconds that records "+
			"a condition rather than a change wakes every webhook forever", len(firing))
	}

	reportUsage(t, a, nodeID, busyReport(id, 10))
	a.alerts.Sweep(context.Background())

	if held := readAlert(t, a, created.ID); held.State != "quiet" {
		t.Fatalf("state = %q, want quiet", held.State)
	}
	cleared := readEvents(t, a, a.secret, "?subject="+id+"&kind=alert.cleared")
	if len(cleared) != 1 {
		t.Fatalf("cleared events = %d, want one: whoever was told it fired has to be told "+
			"it stopped", len(cleared))
	}
}

func TestAWorkloadThatStoppedReportingDoesNotFireABelowAlert(t *testing.T) {
	a, nodeID, tick := newAlertingApp(t)
	id := runningInstance(t, a, a.secret, "web-1", nodeID)

	created := createAlert(t, a, `{"name":"idle","instance_id":"web-1","metric":"cpu",
		"comparison":"below","threshold":5,"for":"1m"}`)

	reportUsage(t, a, nodeID, busyReport(id, 50))
	a.alerts.Sweep(context.Background())

	tick.advance(30 * time.Minute)
	a.alerts.Sweep(context.Background())
	a.alerts.Sweep(context.Background())

	held := readAlert(t, a, created.ID)
	if held.State == "firing" {
		t.Fatal("a node that went quiet fired every below-threshold alert it had. A missing " +
			"reading is not a reading of zero")
	}
	if !strings.Contains(held.Message, "old") {
		t.Fatalf("message = %q, want it to say the reading is stale", held.Message)
	}
}

func TestAnAlertGoesWithItsInstance(t *testing.T) {
	a, nodeID, _ := newAlertingApp(t)
	id := runningInstance(t, a, a.secret, "web-1", nodeID)

	created := createAlert(t, a, `{"name":"hot","instance_id":"web-1","metric":"cpu",
		"threshold":80}`)

	if rec := do(t, a, http.MethodDelete, "/v1/instances/"+id, nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if rec := doAs(t, a, a.secret, http.MethodGet, "/v1/alerts/"+created.ID,
		nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the alert gone with the workload it watched: an alert "+
			"on nothing reads a missing sample forever", rec.Code)
	}
}

func TestAnAlertOnAnotherProjectsWorkloadIsRefused(t *testing.T) {
	a, nodeID, _ := newAlertingApp(t)
	runningInstance(t, a, a.secret, "web-1", nodeID)

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodPost, "/v1/alerts",
		strings.NewReader(`{"name":"peek","instance_id":"web-1","metric":"cpu",
			"threshold":10}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: an alert names a workload, and reading one in "+
			"another project through it is the same leak by another door",
			rec.Code, http.StatusNotFound)
	}
}

func TestAWindowTooShortToMeanAnythingIsRefused(t *testing.T) {
	a, nodeID, _ := newAlertingApp(t)
	runningInstance(t, a, a.secret, "web-1", nodeID)

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/alerts",
		strings.NewReader(`{"name":"twitchy","instance_id":"web-1","metric":"cpu",
			"threshold":80,"for":"1s"}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a one second window fires on a single sample",
			rec.Code, http.StatusBadRequest)
	}
}
