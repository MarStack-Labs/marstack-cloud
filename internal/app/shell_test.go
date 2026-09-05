package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type shellBody struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id"`
	Isolation  string   `json:"isolation"`
	Command    []string `json:"command"`
	State      string   `json:"state"`
	Message    string   `json:"message"`
}

func openShell(t *testing.T, a *testApp, ref, body string) (int, shellBody) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/instances/"+ref+"/shell", strings.NewReader(body))

	var opened shellBody
	_ = json.Unmarshal(rec.Body.Bytes(), &opened)
	return rec.Code, opened
}

func takeShell(t *testing.T, a *testApp, nodeID string) (int, shellBody) {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/shell", nil)

	var taken shellBody
	_ = json.Unmarshal(rec.Body.Bytes(), &taken)
	return rec.Code, taken
}

func streamFrom(t *testing.T, a *testApp, path string) (*http.Response, func()) {
	t.Helper()

	srv := httptest.NewServer(a.Handler())
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.secret)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		srv.Close()
		t.Fatalf("open %s: %v", path, err)
	}
	return res, func() { res.Body.Close(); srv.Close() }
}

func TestWhatTheOperatorTypesReachesTheNode(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")

	code, opened := openShell(t, a, "web-1", `{"command":["/bin/sh"]}`)
	if code != http.StatusCreated {
		t.Fatalf("open: %d", code)
	}

	takenCode, taken := takeShell(t, a, nodeID)
	if takenCode != http.StatusOK {
		t.Fatalf("take: %d", takenCode)
	}
	if taken.ID != opened.ID || taken.Isolation != "container" {
		t.Fatalf("taken = %+v, want the session that was opened", taken)
	}

	down, shut := streamFrom(t, a, "/v1/nodes/"+nodeID+"/shells/"+opened.ID+"/input")
	defer shut()

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		rec := do(t, a, http.MethodPost, "/v1/shells/"+opened.ID+"/input",
			strings.NewReader("echo hello\n"))
		if rec.Code != http.StatusNoContent {
			t.Errorf("send input: %d %s", rec.Code, rec.Body.String())
		}
	}()

	buffer := make([]byte, len("echo hello\n"))
	if _, err := io.ReadFull(down.Body, buffer); err != nil {
		t.Fatalf("read the input stream: %v", err)
	}
	<-sent

	if string(buffer) != "echo hello\n" {
		t.Fatalf("the node read %q, want what was typed. The node has no listener, so this "+
			"stream is the only way keystrokes reach it", buffer)
	}
}

func TestWhatTheShellPrintsReachesTheOperator(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")

	_, opened := openShell(t, a, "web-1", `{}`)
	takeShell(t, a, nodeID)

	up, shut := streamFrom(t, a, "/v1/shells/"+opened.ID+"/output")
	defer shut()

	fromNode, nodeWrites := io.Pipe()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	go func() {
		req, err := http.NewRequest(http.MethodPost,
			srv.URL+"/v1/nodes/"+nodeID+"/shells/"+opened.ID+"/output", fromNode)
		if err != nil {
			t.Errorf("build: %v", err)
			return
		}
		req.Header.Set("Authorization", "Bearer "+a.secret)

		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
		}
	}()

	for _, line := range []string{"first\n", "second\n"} {
		if _, err := nodeWrites.Write([]byte(line)); err != nil {
			t.Fatalf("write %q: %v", line, err)
		}

		buffer := make([]byte, len(line))
		if _, err := io.ReadFull(up.Body, buffer); err != nil {
			t.Fatalf("read %q: %v. Output that only arrives when the stream closes is a "+
				"transcript, not a shell", line, err)
		}
		if string(buffer) != line {
			t.Fatalf("read %q, want %q", buffer, line)
		}
	}

	nodeWrites.Close()
}

func TestASessionIsHandedOutOnlyOnce(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")
	openShell(t, a, "web-1", `{}`)

	var wg sync.WaitGroup
	var taken atomic.Int32

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, _ := takeShell(t, a, nodeID); code == http.StatusOK {
				taken.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := taken.Load(); got != 1 {
		t.Fatalf("%d takers were given the same session. Two nodes attached to one shell "+
			"both write into the same stream, and the operator sees them interleaved", got)
	}
}

func TestAnotherNodeCannotAttachToASession(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")
	_, opened := openShell(t, a, "web-1", `{}`)
	takeShell(t, a, nodeID)

	rec := do(t, a, http.MethodPost,
		"/v1/nodes/n-somewhere-else/shells/"+opened.ID+"/output",
		strings.NewReader("forged"))
	if rec.Code == http.StatusNoContent {
		t.Fatal("another node wrote into a session it was never given, so any node can put " +
			"words on any operator's screen")
	}
}

func TestAVmCannotBeGivenAShell(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "sh", "10.230.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	if code, _ := openShell(t, a, id, `{}`); code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: a vm runs its own kernel, so there is nothing on "+
			"the node to enter", code, http.StatusConflict)
	}
}

func TestClosingASessionEndsTheStreams(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")
	_, opened := openShell(t, a, "web-1", `{}`)
	takeShell(t, a, nodeID)

	up, shut := streamFrom(t, a, "/v1/shells/"+opened.ID+"/output")
	defer shut()

	go func() {
		time.Sleep(50 * time.Millisecond)
		do(t, a, http.MethodDelete, "/v1/shells/"+opened.ID, nil)
	}()

	var read bytes.Buffer
	if _, err := io.Copy(&read, up.Body); err != nil {
		t.Fatalf("copy: %v", err)
	}

	if held := readShell(t, a, opened.ID); held.State != "closed" {
		t.Fatalf("state = %q, want closed: a stream nobody ends holds a request open on the "+
			"control plane forever", held.State)
	}
}

func readShell(t *testing.T, a *testApp, id string) shellBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/shells/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read session: %d %s", rec.Code, rec.Body.String())
	}

	var held shellBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return held
}

func TestAQueueOfShellsNobodyOpensIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	runningContainer(t, a, nodeID, "web-1")

	refused := 0
	for range 8 {
		if code, _ := openShell(t, a, "web-1", `{}`); code == http.StatusTooManyRequests {
			refused++
		}
	}

	if refused == 0 {
		t.Fatal("eight shells were opened with nothing taking them. Each one holds a " +
			"process on a node for as long as it lives")
	}
}
