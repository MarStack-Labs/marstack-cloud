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
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

type envBody struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	EnvNames []string          `json:"env_names"`
	Env      map[string]string `json:"env"`
}

type envListBody struct {
	Instances []envBody `json:"instances"`
}

func createWithEnv(t *testing.T, a *testApp, name, env string) string {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"container","image":"alpine:3.20","env":` + env + `}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created envBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func operatorView(t *testing.T, a *testApp, id string) envBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/instances/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}

	var got envBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func nodeView(t *testing.T, a *testApp, nodeID, id string) envBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/instances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node view: %d %s", rec.Code, rec.Body.String())
	}

	var list envListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, in := range list.Instances {
		if in.ID == id {
			return in
		}
	}
	t.Fatalf("instance %s is not in the node's view", id)
	return envBody{}
}

func TestTheOperatorSeesNamesAndTheNodeSeesValues(t *testing.T) {
	a, nodeID := newSealingApp(t)
	id := createWithEnv(t, a, "web", `{"DATABASE_URL":"postgres://secret","PORT":"8080"}`)
	placement(t, a, id)

	got := operatorView(t, a, id)
	if len(got.Env) != 0 {
		t.Fatalf("env = %v, want the operator route never to serve values back", got.Env)
	}
	if strings.Join(got.EnvNames, ",") != "DATABASE_URL,PORT" {
		t.Fatalf("env_names = %v, want both names sorted", got.EnvNames)
	}

	fromNode := nodeView(t, a, nodeID, id)
	if fromNode.Env["DATABASE_URL"] != "postgres://secret" || fromNode.Env["PORT"] != "8080" {
		t.Fatalf("the node saw %v, want the values it has to run with", fromNode.Env)
	}
}

func TestAValueNeverAppearsInTheResponseBody(t *testing.T) {
	a, _ := newSealingApp(t)

	body := `{"name":"web","isolation":"container","image":"alpine:3.20",` +
		`"env":{"TOKEN":"hunter2-do-not-echo"}}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hunter2-do-not-echo") {
		t.Fatal("the create response echoed the value back, so it is in every client's log")
	}

	list := do(t, a, http.MethodGet, "/v1/instances", nil)
	if strings.Contains(list.Body.String(), "hunter2-do-not-echo") {
		t.Fatal("the listing carried the value")
	}
}

func newSealingApp(t *testing.T) (*testApp, string) {
	t.Helper()
	a, _ := sealingAppIn(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.scheduler.Run(ctx)

	return a, registerNode(t, a, "bm-1", "rack-a")
}

func sealingAppIn(t *testing.T, dir string) (*testApp, string) {
	t.Helper()

	k, _ := sealed.NewKey()
	built, err := New(context.Background(),
		Config{
			DataDir:           dir,
			BackupKeys:        []sealed.Key{k},
			SchedulerInterval: 10 * time.Millisecond,
			RatePerSecond:     &unlimited,
		}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { built.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}
	return &testApp{App: built, secret: strings.TrimSpace(string(raw))}, dir
}

func TestEnvIsRefusedWithNoKeyToSealItWith(t *testing.T) {
	a, _ := newBalancingApp(t)

	body := `{"name":"web","isolation":"container","image":"alpine:3.20",` +
		`"env":{"TOKEN":"hunter2"}}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: a control plane with no key must not take credentials "+
			"it can only store in the clear", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "backup-key-file") {
		t.Fatalf("body = %s, want it to name the flag that fixes this", rec.Body.String())
	}
}

func TestAnInstanceWithNoEnvNeedsNoKey(t *testing.T) {
	a, _ := newBalancingApp(t)

	body := `{"name":"plain","isolation":"container","image":"alpine:3.20"}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: the refusal must be about env, not about instances",
			rec.Code, http.StatusCreated)
	}
}

func TestAValueIsNotOnDiskInTheClear(t *testing.T) {
	a, dir := sealingAppIn(t, t.TempDir())

	registerNode(t, a, "bm-1", "rack-a")
	createWithEnv(t, a, "web", `{"TOKEN":"hunter2-not-on-disk"}`)

	onDisk := everythingUnder(t, dir)

	if strings.Contains(onDisk, "hunter2-not-on-disk") {
		t.Fatal("the value is on the control plane disk in the clear, so a stolen database " +
			"gives up every credential an instance runs with")
	}
	if !strings.Contains(onDisk, "TOKEN") {
		t.Fatal("the name is not stored either, which would make listing names need the key")
	}
}

func everythingUnder(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the data directory: %v", err)
	}

	var all strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		all.Write(blob)
	}
	return all.String()
}

func TestABadEnvIsRefused(t *testing.T) {
	a, _ := newSealingApp(t)

	for _, env := range []string{
		`{"1BAD":"x"}`,
		`{"has space":"x"}`,
		`{"has-hyphen":"x"}`,
		`{"":"x"}`,
	} {
		body := `{"name":"bad","isolation":"container","image":"alpine:3.20","env":` + env + `}`
		rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", env, rec.Code, http.StatusBadRequest)
		}
	}
}
