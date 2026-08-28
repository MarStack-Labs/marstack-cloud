package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type received struct {
	Kind      string
	Signature string
	Body      []byte
}

type sink struct {
	mu     sync.Mutex
	got    []received
	status int
}

func (s *sink) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		s.mu.Lock()
		s.got = append(s.got, received{
			Kind:      r.Header.Get("Marstack-Event"),
			Signature: r.Header.Get("Marstack-Signature"),
			Body:      body,
		})
		status := s.status
		s.mu.Unlock()

		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	})
}

func (s *sink) seen() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.got...)
}

type webhookBody struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Kinds  []string `json:"kinds"`
	Active bool     `json:"active"`
	Secret string   `json:"secret"`
}

type deliveryBody struct {
	ID        string `json:"id"`
	EventID   int64  `json:"event_id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
}

func createWebhook(t *testing.T, a *testApp, secret, body string) webhookBody {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodPost, "/v1/webhooks", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create webhook: %d %s", rec.Code, rec.Body.String())
	}

	var created webhookBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func readDeliveries(t *testing.T, a *testApp, id string) []deliveryBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/webhooks/"+id+"/deliveries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read deliveries: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Deliveries []deliveryBody `json:"deliveries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Deliveries
}

func pump(t *testing.T, a *testApp, times int) {
	t.Helper()

	for range times {
		a.webhooks.Pump(context.Background())
	}
}

func reachingLoopback(t *testing.T, a *testApp) {
	t.Helper()
	a.webhooks.AllowLoopbackBecauseThisIsATest()
}

func makeAnEvent(t *testing.T, a *testApp, nodeID, name, state string) string {
	t.Helper()

	id := newInstance(t, a, a.secret, name)
	if placed := waitForPlacement(t, a, id); placed == "" {
		t.Fatal("never placed")
	}
	reportState(t, a, nodeID, id, `{"observed_state":"`+state+`"}`)
	return id
}

func TestAnEventReachesASubscriber(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	target := httptest.NewServer((&sink{}).handler())
	defer target.Close()

	box := &sink{}
	target.Config.Handler = box.handler()

	reachingLoopback(t, a)
	hook := createWebhook(t, a, a.secret,
		`{"name":"ops","url":"`+target.URL+`/hook"}`)
	if hook.Secret == "" {
		t.Fatal("a webhook was created without showing its signing secret")
	}

	pump(t, a, 1)
	id := makeAnEvent(t, a, nodeID, "web-1", "running")
	pump(t, a, 2)

	seen := box.seen()
	if len(seen) == 0 {
		t.Fatal("nothing was delivered")
	}

	var carried bool
	for _, got := range seen {
		if got.Kind == "instance.running" {
			carried = true
		}
		want := hmac.New(sha256.New, []byte(hook.Secret))
		want.Write(got.Body)
		if got.Signature != "sha256="+hex.EncodeToString(want.Sum(nil)) {
			t.Fatalf("signature = %q, want it to verify against the secret", got.Signature)
		}
	}
	if !carried {
		t.Fatalf("deliveries = %+v, want the transition of %s", seen, id)
	}

	for _, delivery := range readDeliveries(t, a, hook.ID) {
		if delivery.State != "delivered" {
			t.Fatalf("delivery = %+v, want it marked delivered", delivery)
		}
	}
}

func TestOnlyTheSubscribedKindsAreDelivered(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	box := &sink{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	reachingLoopback(t, a)
	createWebhook(t, a, a.secret,
		`{"name":"failures","url":"`+target.URL+`/hook","kinds":["instance.failed"]}`)

	pump(t, a, 1)
	makeAnEvent(t, a, nodeID, "web-1", "running")
	pump(t, a, 2)

	if seen := box.seen(); len(seen) != 0 {
		t.Fatalf("delivered %+v, want nothing: only instance.failed was subscribed", seen)
	}

	id := newInstance(t, a, a.secret, "web-2")
	waitForPlacement(t, a, id)
	reportState(t, a, nodeID, id, `{"observed_state":"failed","message":"died"}`)
	pump(t, a, 2)

	seen := box.seen()
	if len(seen) != 1 || seen[0].Kind != "instance.failed" {
		t.Fatalf("delivered %+v, want exactly the failure", seen)
	}
}

func TestAWildcardKindMatchesAFamily(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	box := &sink{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	reachingLoopback(t, a)
	createWebhook(t, a, a.secret,
		`{"name":"all-instances","url":"`+target.URL+`/hook","kinds":["instance.*"]}`)

	pump(t, a, 1)
	makeAnEvent(t, a, nodeID, "web-1", "running")
	pump(t, a, 2)

	if seen := box.seen(); len(seen) == 0 {
		t.Fatal("instance.* did not match instance.running")
	}
}

func TestASubscriberIsRetriedAndThenGivenUpOn(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	box := &sink{status: http.StatusInternalServerError}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	reachingLoopback(t, a)
	hook := createWebhook(t, a, a.secret, `{"name":"broken","url":"`+target.URL+`/hook"}`)

	pump(t, a, 1)
	makeAnEvent(t, a, nodeID, "web-1", "running")
	pump(t, a, 1)

	deliveries := readDeliveries(t, a, hook.ID)
	if len(deliveries) == 0 {
		t.Fatal("nothing was queued")
	}
	first := deliveries[0]
	if first.State != "pending" || first.Attempts != 1 {
		t.Fatalf("delivery = %+v, want one failed attempt still pending", first)
	}
	if !strings.Contains(first.LastError, "500") {
		t.Fatalf("last_error = %q, want it to name the status", first.LastError)
	}
}

func TestAPausedWebhookStopsReceiving(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	box := &sink{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	reachingLoopback(t, a)
	hook := createWebhook(t, a, a.secret, `{"name":"ops","url":"`+target.URL+`/hook"}`)
	pump(t, a, 1)

	rec := do(t, a, http.MethodPost, "/v1/webhooks/"+hook.ID+"/pause", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body.String())
	}

	makeAnEvent(t, a, nodeID, "web-1", "running")
	pump(t, a, 2)

	if seen := box.seen(); len(seen) != 0 {
		t.Fatalf("delivered %+v to a paused webhook", seen)
	}

	if rec := do(t, a, http.MethodPost, "/v1/webhooks/"+hook.ID+"/resume",
		nil); rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
}

func TestASubscriberOnlyHearsAboutItsOwnProject(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	box := &sink{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	reachingLoopback(t, a)
	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	createWebhook(t, a, other, `{"name":"nosy","url":"`+target.URL+`/hook"}`)

	pump(t, a, 1)
	makeAnEvent(t, a, nodeID, "web-1", "running")
	pump(t, a, 2)

	if seen := box.seen(); len(seen) != 0 {
		t.Fatalf("delivered %+v across a project boundary", seen)
	}
}

func TestANewWebhookDoesNotReceiveHistory(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	makeAnEvent(t, a, nodeID, "web-1", "running")

	box := &sink{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	reachingLoopback(t, a)
	createWebhook(t, a, a.secret, `{"name":"ops","url":"`+target.URL+`/hook"}`)
	pump(t, a, 3)

	if seen := box.seen(); len(seen) != 0 {
		t.Fatalf("delivered %+v, want a new subscription to start from now", seen)
	}
}

func TestTheSecretIsShownOnceAndNeverAgain(t *testing.T) {
	a, _ := newBalancingApp(t)

	box := &sink{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	hook := createWebhook(t, a, a.secret, `{"name":"ops","url":"`+target.URL+`/hook"}`)
	if hook.Secret == "" {
		t.Fatal("the secret was not shown on create, so it can never be used")
	}

	rec := do(t, a, http.MethodGet, "/v1/webhooks/"+hook.ID, nil)
	var later webhookBody
	if err := json.Unmarshal(rec.Body.Bytes(), &later); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if later.Secret != "" {
		t.Fatal("reading a webhook handed the signing secret out again")
	}

	rec = do(t, a, http.MethodGet, "/v1/webhooks", nil)
	if strings.Contains(rec.Body.String(), "whsec_") {
		t.Fatal("listing webhooks leaked a signing secret")
	}
}

func TestAWebhookPointedAtTheControlPlaneIsRefusedAtDial(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	hook := createWebhook(t, a, a.secret,
		`{"name":"loopback","url":"http://127.0.0.1:7443/v1/projects"}`)

	pump(t, a, 1)
	makeAnEvent(t, a, nodeID, "web-1", "running")
	pump(t, a, 1)

	deliveries := readDeliveries(t, a, hook.ID)
	if len(deliveries) == 0 {
		t.Fatal("nothing was queued")
	}
	if !strings.Contains(deliveries[0].LastError, "loopback") {
		t.Fatalf("last_error = %q, want the dial refused rather than the post attempted",
			deliveries[0].LastError)
	}
}

func TestAWebhookIsInvisibleFromAnotherProject(t *testing.T) {
	a, _ := newBalancingApp(t)

	box := &sink{}
	target := httptest.NewServer(box.handler())
	defer target.Close()

	createWebhook(t, a, a.secret, `{"name":"ops","url":"`+target.URL+`/hook"}`)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	if rec := doAs(t, a, other, http.MethodGet, "/v1/webhooks/ops",
		nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestABadWebhookShapeIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	refused := map[string]string{
		"no url":            `{"name":"a"}`,
		"a bad scheme":      `{"name":"b","url":"ftp://example.com/x"}`,
		"an empty name":     `{"name":"","url":"http://example.com/x"}`,
		"a bad wildcard":    `{"name":"c","url":"http://example.com/x","kinds":["inst*nce"]}`,
		"whitespace a kind": `{"name":"d","url":"http://example.com/x","kinds":["a b"]}`,
	}

	for what, body := range refused {
		rec := do(t, a, http.MethodPost, "/v1/webhooks", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d (%s)", what, rec.Code,
				http.StatusBadRequest, rec.Body.String())
		}
	}
}
