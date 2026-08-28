package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type eventBody struct {
	ID       int64  `json:"id"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	NodeID   string `json:"node_id"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

func readEvents(t *testing.T, a *testApp, secret, query string) []eventBody {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodGet, "/v1/events"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read events: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Events []eventBody `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Events
}

func reportState(t *testing.T, a *testApp, nodeID, instanceID, body string) {
	t.Helper()

	rec := do(t, a, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/status", strings.NewReader(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAWorkloadComingUpIsRecorded(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	if placed := waitForPlacement(t, a, id); placed == "" {
		t.Fatal("never placed")
	}
	reportState(t, a, nodeID, id, `{"observed_state":"running"}`)

	entries := readEvents(t, a, a.secret, "?subject="+id)
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want the one transition", entries)
	}
	if entries[0].Kind != "instance.running" || entries[0].Severity != "info" {
		t.Fatalf("event = %+v", entries[0])
	}
	if entries[0].NodeID != nodeID {
		t.Fatalf("node_id = %q, want the node it happened on", entries[0].NodeID)
	}
}

func TestAWorkloadThatDiesCarriesTheReasonTheNodeGave(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)
	reportState(t, a, nodeID, id, `{"observed_state":"running"}`)
	reportState(t, a, nodeID, id,
		`{"observed_state":"failed","message":"exited with code 127: httpd: not found"}`)

	entries := readEvents(t, a, a.secret, "?subject="+id+"&severity=error")
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want the failure", entries)
	}
	if entries[0].Kind != "instance.failed" {
		t.Fatalf("kind = %q", entries[0].Kind)
	}
	if !strings.Contains(entries[0].Message, "httpd: not found") {
		t.Fatalf("message = %q, want the reason the node gave, which is the whole point",
			entries[0].Message)
	}
}

func TestARestartIsRecordedEvenWhenTheStateLooksUnchanged(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)
	reportState(t, a, nodeID, id, `{"observed_state":"running"}`)
	reportState(t, a, nodeID, id,
		`{"observed_state":"running","message":"restarted after exiting","restarts":1}`)

	entries := readEvents(t, a, a.secret, "?subject="+id+"&kind=instance.restarted")
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want the restart even though observed stayed running", entries)
	}
	if entries[0].Severity != "warn" {
		t.Fatalf("severity = %q, want warn", entries[0].Severity)
	}
	if !strings.Contains(entries[0].Message, "restart 1") {
		t.Fatalf("message = %q, want the attempt number", entries[0].Message)
	}
}

func TestRepeatingTheSameStateRecordsNothing(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)

	for range 4 {
		reportState(t, a, nodeID, id, `{"observed_state":"running"}`)
	}

	if entries := readEvents(t, a, a.secret, "?subject="+id); len(entries) != 1 {
		t.Fatalf("events = %d, want one rather than one per report", len(entries))
	}
}

func TestABackendLeavingABalancerIsRecorded(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	lb := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"tcp","instances":["`+id+`"]}`)

	reportHealth(t, a, nodeID, `{"checks":[{"balancer_id":"`+lb.ID+
		`","instance_id":"`+id+`","healthy":true}]}`)
	reportHealth(t, a, nodeID, `{"checks":[{"balancer_id":"`+lb.ID+
		`","instance_id":"`+id+`","healthy":false,"reason":"tcp connect refused"}]}`)

	entries := readEvents(t, a, a.secret, "?subject="+id+"&kind=backend.down")
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want the backend going down", entries)
	}
	if !strings.Contains(entries[0].Message, "tcp connect refused") {
		t.Fatalf("message = %q, want why it left", entries[0].Message)
	}

	if up := readEvents(t, a, a.secret, "?subject="+id+"&kind=backend.up"); len(up) != 1 {
		t.Fatalf("events = %+v, want it coming up recorded too", up)
	}
}

func TestARepeatedHealthReportRecordsNothing(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := runningInstance(t, a, a.secret, "web-1", nodeID)
	lb := createBalancer(t, a, a.secret, `{"name":"web","target_port":80,"listen_port":8080,
		"check":"tcp","instances":["`+id+`"]}`)

	for range 5 {
		reportHealth(t, a, nodeID, `{"checks":[{"balancer_id":"`+lb.ID+
			`","instance_id":"`+id+`","healthy":true}]}`)
	}

	entries := readEvents(t, a, a.secret, "?subject="+id+"&kind=backend.up")
	if len(entries) != 1 {
		t.Fatalf("events = %d, want one flip rather than one per report", len(entries))
	}
}

func TestEventsAreScopedToTheirProject(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)
	reportState(t, a, nodeID, id, `{"observed_state":"running"}`)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))

	if entries := readEvents(t, a, other, ""); len(entries) != 0 {
		t.Fatalf("events = %+v, want none from another project", entries)
	}
	if entries := readEvents(t, a, other, "?subject="+id); len(entries) != 0 {
		t.Fatalf("naming the id reached across the project boundary: %+v", entries)
	}
}

func TestAViewerCanReadEvents(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)
	reportState(t, a, nodeID, id, `{"observed_state":"running"}`)

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"looker","role":"viewer"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if entries := readEvents(t, a, created.Secret, ""); len(entries) == 0 {
		t.Fatal("a viewer cannot see events, which is the one thing a viewer is for")
	}
}

func TestAnUnknownSeverityIsRefusedRatherThanIgnored(t *testing.T) {
	a, _ := newBalancingApp(t)

	rec := do(t, a, http.MethodGet, "/v1/events?severity=critical", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d so a typo does not silently return everything",
			rec.Code, http.StatusBadRequest)
	}
}

func TestEventsComeBackNewestFirst(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)
	reportState(t, a, nodeID, id, `{"observed_state":"running"}`)
	reportState(t, a, nodeID, id, `{"observed_state":"failed","message":"died"}`)

	entries := readEvents(t, a, a.secret, "?subject="+id)
	if len(entries) != 2 {
		t.Fatalf("events = %d, want both transitions", len(entries))
	}
	if entries[0].Kind != "instance.failed" {
		t.Fatalf("first = %q, want the newest first", entries[0].Kind)
	}
}

func TestAnAgentForgettingItsRestartCountIsNotATransition(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)
	reportState(t, a, nodeID, id, `{"observed_state":"running"}`)
	reportState(t, a, nodeID, id,
		`{"observed_state":"running","message":"restarted after exiting","restarts":3}`)

	reportState(t, a, nodeID, id,
		`{"observed_state":"running","message":"adopted after an agent restart","restarts":0}`)

	entries := readEvents(t, a, a.secret, "?subject="+id)
	for _, entry := range entries {
		if strings.Contains(entry.Message, "adopted") {
			t.Fatalf("an agent restart forgetting its counters was recorded as %q; "+
				"nothing happened to the workload", entry.Kind)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("events = %d, want the two real transitions", len(entries))
	}
}
