package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/platform/exec"
)

type execBody struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id"`
	Isolation  string   `json:"isolation"`
	Command    []string `json:"command"`
	State      string   `json:"state"`
	Output     string   `json:"output"`
	Truncated  bool     `json:"truncated"`
	ExitCode   *int     `json:"exit_code"`
	Message    string   `json:"message"`
	Timeout    string   `json:"timeout"`
}

func startExec(t *testing.T, a *testApp, ref, body string) (int, execBody) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/instances/"+ref+"/exec", strings.NewReader(body))

	var started execBody
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	return rec.Code, started
}

func readExec(t *testing.T, a *testApp, id string) (int, execBody) {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/execs/"+id, nil)

	var held execBody
	_ = json.Unmarshal(rec.Body.Bytes(), &held)
	return rec.Code, held
}

func takeCommand(t *testing.T, a *testApp, nodeID string) (int, execBody) {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/exec", nil)

	var taken execBody
	_ = json.Unmarshal(rec.Body.Bytes(), &taken)
	return rec.Code, taken
}

func finishCommand(t *testing.T, a *testApp, nodeID, id, body string) int {
	t.Helper()

	rec := do(t, a, http.MethodPost,
		"/v1/nodes/"+nodeID+"/execs/"+id+"/result", strings.NewReader(body))
	return rec.Code
}

func runningContainer(t *testing.T, a *testApp, nodeID, name string) string {
	t.Helper()

	id := newInstance(t, a, a.secret, name)
	if placed := waitForPlacement(t, a, id); placed == "" {
		t.Fatalf("instance %s was never placed", id)
	}
	reportRunning(t, a, nodeID, id)
	return id
}

func TestACommandGoesToTheNodeAndComesBack(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := runningContainer(t, a, nodeID, "web-1")

	code, started := startExec(t, a, "web-1", `{"command":["ls","/"]}`)
	if code != http.StatusCreated {
		t.Fatalf("start: %d", code)
	}
	if started.State != "waiting" {
		t.Fatalf("state = %q, want waiting", started.State)
	}

	takenCode, taken := takeCommand(t, a, nodeID)
	if takenCode != http.StatusOK {
		t.Fatalf("take: %d", takenCode)
	}
	if taken.ID != started.ID || taken.InstanceID != id {
		t.Fatalf("taken = %+v, want the command that was started", taken)
	}
	if taken.Isolation != "container" {
		t.Fatalf("isolation = %q, want the node told which runtime to use",
			taken.Isolation)
	}

	if code := finishCommand(t, a, nodeID, started.ID,
		`{"output":"bin dev etc\n","exit_code":0}`); code != http.StatusNoContent {
		t.Fatalf("finish: %d", code)
	}

	_, held := readExec(t, a, started.ID)
	if held.State != "done" || held.Output != "bin dev etc\n" {
		t.Fatalf("held = %+v, want the output that came back", held)
	}
	if held.ExitCode == nil || *held.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", held.ExitCode)
	}
}

func TestACommandIsHandedOutOnlyOnce(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")
	startExec(t, a, "web-1", `{"command":["true"]}`)

	if code, _ := takeCommand(t, a, nodeID); code != http.StatusOK {
		t.Fatalf("first take: %d", code)
	}
	if code, _ := takeCommand(t, a, nodeID); code != http.StatusNoContent {
		t.Fatalf("second take: %d, want nothing left. A command handed to two passes runs "+
			"twice, and running something twice is not what anybody asked for", code)
	}
}

func TestOnlyTheNodeHoldingTheWorkloadIsGivenTheCommand(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")
	_, started := startExec(t, a, "web-1", `{"command":["true"]}`)

	if code, _ := takeCommand(t, a, "n-somewhere-else"); code == http.StatusOK {
		t.Fatal("another node was handed a command for a workload it does not hold")
	}
	if code := finishCommand(t, a, "n-somewhere-else", started.ID,
		`{"output":"forged","exit_code":0}`); code == http.StatusNoContent {
		t.Fatal("another node answered for a command it was never given, so any node can " +
			"put words in the output of any other")
	}
}

func TestAVmCannotBeEntered(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "ex", "10.230.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	code, _ := startExec(t, a, id, `{"command":["ls"]}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: a vm runs its own kernel, so there is no way in "+
			"from the node and pretending otherwise would hand back an empty answer",
			code, http.StatusConflict)
	}
}

func TestAWorkloadThatIsNotRunningCannotBeEntered(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := newInstance(t, a, a.secret, "web-1")
	waitForPlacement(t, a, id)

	if code, _ := startExec(t, a, "web-1", `{"command":["ls"]}`); code !=
		http.StatusConflict {
		t.Fatalf("status = %d, want %d: there is nothing to enter yet", code,
			http.StatusConflict)
	}
	_ = nodeID
}

func TestACommandWithNothingInItIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")

	for _, body := range []string{`{"command":[]}`, `{}`} {
		if code, _ := startExec(t, a, "web-1", body); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", body, code, http.StatusBadRequest)
		}
	}
}

func TestATimeoutOutsideTheBoundsIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")

	for _, body := range []string{
		`{"command":["true"],"timeout":"1ms"}`,
		`{"command":["true"],"timeout":"24h"}`,
		`{"command":["true"],"timeout":"nonsense"}`,
	} {
		if code, _ := startExec(t, a, "web-1", body); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d: a command that may run for a day holds a "+
				"slot on the node for a day", body, code, http.StatusBadRequest)
		}
	}
}

func TestAQueueNobodyDrainsIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")

	refused := 0
	for range exec.MaxWaitingPerProject + 4 {
		if code, _ := startExec(t, a, "web-1", `{"command":["true"]}`); code ==
			http.StatusTooManyRequests {
			refused++
		}
	}

	if refused == 0 {
		t.Fatalf("%d commands were queued with nothing taking them, so a caller can fill "+
			"the database by asking", exec.MaxWaitingPerProject+4)
	}
}

func TestTheOutputIsCappedAndTheEndIsKept(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")
	_, started := startExec(t, a, "web-1", `{"command":["true"]}`)
	takeCommand(t, a, nodeID)

	huge := strings.Repeat("a", exec.MaxOutputBytes) + "THE END"
	body, err := json.Marshal(map[string]any{"output": huge, "exit_code": 0})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	finishCommand(t, a, nodeID, started.ID, string(body))

	_, held := readExec(t, a, started.ID)
	if len(held.Output) > exec.MaxOutputBytes {
		t.Fatalf("output is %d bytes, want at most %d", len(held.Output),
			exec.MaxOutputBytes)
	}
	if !held.Truncated {
		t.Fatal("the output was cut and nothing says so")
	}
	if !strings.HasSuffix(held.Output, "THE END") {
		t.Fatal("the end of the output was thrown away. What a command was going to tell " +
			"you is at the end, the same as a log")
	}
}

func TestAnotherProjectCannotReadACommand(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")
	_, started := startExec(t, a, "web-1", `{"command":["true"]}`)
	takeCommand(t, a, nodeID)
	finishCommand(t, a, nodeID, started.ID, `{"output":"secret","exit_code":0}`)

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodGet, "/v1/execs/"+started.ID, nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("the refusal carried the output with it")
	}
}

func TestOldCommandsAreForgotten(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")

	first := ""
	for i := range exec.KeepPerInstance + 5 {
		_, started := startExec(t, a, "web-1",
			`{"command":["echo","`+strconv.Itoa(i)+`"]}`)
		if i == 0 {
			first = started.ID
		}
		takeCommand(t, a, nodeID)
		finishCommand(t, a, nodeID, started.ID, `{"output":"done","exit_code":0}`)
	}

	if code, _ := readExec(t, a, first); code != http.StatusNotFound {
		t.Fatalf("status = %d, want the oldest gone: one row per command with nothing "+
			"trimming them grows for as long as the platform runs", code)
	}
}

func TestDeletingAnInstanceForgetsItsCommands(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := runningContainer(t, a, nodeID, "web-1")
	_, started := startExec(t, a, "web-1", `{"command":["true"]}`)

	if rec := do(t, a, http.MethodDelete, "/v1/instances/"+id, nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if code, _ := readExec(t, a, started.ID); code != http.StatusNotFound {
		t.Fatalf("status = %d, want the command gone with the instance it was for", code)
	}
}
