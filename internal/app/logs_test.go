package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/platform/logs"
)

type logTailBody struct {
	InstanceID string `json:"instance_id"`
	Truncated  bool   `json:"truncated"`
	Lines      []struct {
		Seq  int64  `json:"seq"`
		Text string `json:"text"`
	} `json:"lines"`
}

func shipLines(t *testing.T, a *testApp, secret, nodeID, instanceID string, texts []string) int {
	t.Helper()

	body, err := json.Marshal(struct {
		Lines []string `json:"lines"`
	}{Lines: texts})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	rec := doAs(t, a, secret, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/logs", strings.NewReader(string(body)))
	return rec.Code
}

func readLogs(t *testing.T, a *testApp, instanceID, query string) (int, logTailBody) {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/instances/"+instanceID+"/logs"+query, nil)

	var body logTailBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func placedInstance(t *testing.T, a *testApp, name string) (string, string) {
	t.Helper()

	id := newInstance(t, a, a.secret, name)
	nodeID := waitForPlacement(t, a, id)
	if nodeID == "" {
		t.Fatalf("instance %s was never placed", id)
	}
	return id, nodeID
}

func TestANodeShipsWhatAWorkloadPrinted(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")

	if code := shipLines(t, a, a.secret, nodeID, id,
		[]string{"listening on 8080", "ready"}); code != http.StatusNoContent {
		t.Fatalf("ship: %d", code)
	}

	code, body := readLogs(t, a, id, "")
	if code != http.StatusOK {
		t.Fatalf("read: %d", code)
	}
	if len(body.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(body.Lines))
	}
	if body.Lines[0].Text != "listening on 8080" || body.Lines[1].Text != "ready" {
		t.Fatalf("lines = %+v, want them oldest first: reading a log backwards is useless",
			body.Lines)
	}
}

func TestALogKeepsTheEndRatherThanTheStart(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")

	for batch := range 6 {
		texts := make([]string, 0, logs.MaxLinesPerReport)
		for i := range logs.MaxLinesPerReport {
			texts = append(texts, "line "+strconv.Itoa(batch*logs.MaxLinesPerReport+i))
		}
		if code := shipLines(t, a, a.secret, nodeID, id, texts); code != http.StatusNoContent {
			t.Fatalf("batch %d: %d", batch, code)
		}
	}

	_, body := readLogs(t, a, id, "?tail="+strconv.Itoa(logs.MaxTail))
	last := body.Lines[len(body.Lines)-1].Text
	if last != "line "+strconv.Itoa(6*logs.MaxLinesPerReport-1) {
		t.Fatalf("last = %q, want the newest line: the reason something died is at the end",
			last)
	}
}

func TestTailReturnsNoMoreThanTheCapHoweverMuchIsThere(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")

	for batch := range 4 {
		texts := make([]string, 0, logs.MaxLinesPerReport)
		for i := range logs.MaxLinesPerReport {
			texts = append(texts, "line "+strconv.Itoa(batch*logs.MaxLinesPerReport+i))
		}
		shipLines(t, a, a.secret, nodeID, id, texts)
	}

	code, body := readLogs(t, a, id, "?tail=999999")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want the ask capped rather than refused", code)
	}
	if len(body.Lines) != logs.MaxTail {
		t.Fatalf("lines = %d, want exactly %d: an uncapped tail lets one request pull "+
			"everything the platform kept through the single database connection",
			len(body.Lines), logs.MaxTail)
	}
}

func TestTooManyLinesInOneReportIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")

	texts := make([]string, logs.MaxLinesPerReport+1)
	for i := range texts {
		texts[i] = "x"
	}

	if code := shipLines(t, a, a.secret, nodeID, id, texts); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: one request must not be able to ask for unbounded "+
			"work on the single database connection", code, http.StatusBadRequest)
	}
}

func TestAVeryLongLineIsCutRatherThanStored(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")

	shipLines(t, a, a.secret, nodeID, id, []string{strings.Repeat("a", 10_000)})

	_, body := readLogs(t, a, id, "")
	if len(body.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(body.Lines))
	}
	if len(body.Lines[0].Text) > logs.MaxLineBytes {
		t.Fatalf("line is %d bytes, want at most %d", len(body.Lines[0].Text), logs.MaxLineBytes)
	}
}

func TestATailThatIsNotANumberIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")
	shipLines(t, a, a.secret, nodeID, id, []string{"one"})

	if code, _ := readLogs(t, a, id, "?tail=nonsense"); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", code, http.StatusBadRequest)
	}
}

func TestANodeCannotShipForAnInstanceItDoesNotHold(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")

	other := "n-somewhere-else"
	if code := shipLines(t, a, a.secret, other, id,
		[]string{"forged"}); code == http.StatusNoContent {
		t.Fatal("any node could write to any instance's log, so a compromised node can put " +
			"words in another node's workload")
	}

	_ = nodeID
}

func TestAnotherProjectsLogIsNotFound(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")
	shipLines(t, a, a.secret, nodeID, id, []string{"secret"})

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodGet, "/v1/instances/"+id+"/logs", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("the refusal carried the log with it")
	}
}

func TestDeletingAnInstanceForgetsItsLog(t *testing.T) {
	a, _ := newBalancingApp(t)
	id, nodeID := placedInstance(t, a, "web-1")
	shipLines(t, a, a.secret, nodeID, id, []string{"remembered"})

	if rec := do(t, a, http.MethodDelete, "/v1/instances/"+id, nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	second, secondNode := placedInstance(t, a, "web-2")
	shipLines(t, a, a.secret, secondNode, second, []string{"fresh"})

	_, body := readLogs(t, a, second, "")
	for _, line := range body.Lines {
		if line.Text == "remembered" {
			t.Fatal("a deleted instance's log outlived it")
		}
	}
}
