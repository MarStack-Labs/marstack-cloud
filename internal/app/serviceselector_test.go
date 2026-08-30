package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func createSelectedService(t *testing.T, a *testApp, name, selector string, replicas int) string {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"container","image":"alpine:3.20",` +
		`"replicas":` + strconv.Itoa(replicas)
	if selector != "" {
		body += `,"node_selector":` + selector
	}
	body += `}`

	rec := do(t, a, http.MethodPost, "/v1/services", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create service: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID           string            `json:"id"`
		NodeSelector map[string]string `json:"node_selector"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if selector != "" && len(created.NodeSelector) == 0 {
		t.Fatal("the response dropped the selector, so an operator cannot read back what " +
			"the service was told to do")
	}
	return created.ID
}

func replicaNodes(t *testing.T, a *testApp, serviceID string, want int) []string {
	t.Helper()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		settle(t, a, 1)

		rec := do(t, a, http.MethodGet, "/v1/services/"+serviceID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get service: %d %s", rec.Code, rec.Body.String())
		}

		var body struct {
			Members []struct {
				InstanceID string `json:"instance_id"`
			} `json:"members"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if len(body.Members) >= want {
			nodes := make([]string, 0, len(body.Members))
			settled := true

			for _, m := range body.Members {
				got := placement(t, a, m.InstanceID)
				if got.NodeID == "" && got.Message == "" {
					settled = false
					break
				}
				nodes = append(nodes, got.NodeID)
			}
			if settled {
				return nodes
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil
}

func TestEveryReplicaLandsOnAMatchingNode(t *testing.T) {
	a, first := newBalancingApp(t)
	second := registerNode(t, a, "bm-2", "rack-b")

	setLabels(t, a, a.secret, first, `{"labels":{"tier":"prod"}}`)
	setLabels(t, a, a.secret, second, `{"labels":{"tier":"dev"}}`)

	id := createSelectedService(t, a, "pool", `{"tier":"dev"}`, 3)

	nodes := replicaNodes(t, a, id, 3)
	if len(nodes) < 3 {
		t.Fatalf("the service never reached three replicas, got %d", len(nodes))
	}
	for _, node := range nodes {
		if node != second {
			t.Fatalf("a replica landed on %s, want every one on the node carrying tier=dev "+
				"(%s): a selector the template forgets is a selector that does nothing",
				node, second)
		}
	}
}

func TestAServiceWithNoSelectorSpreadsAsBefore(t *testing.T) {
	a, _ := newBalancingApp(t)
	registerNode(t, a, "bm-2", "rack-b")

	id := createSelectedService(t, a, "pool", "", 2)
	if nodes := replicaNodes(t, a, id, 2); len(nodes) < 2 {
		t.Fatalf("a service with no selector did not reach its replicas: %v", nodes)
	}
}

func TestAnUnmatchableSelectorLeavesTheServiceWithoutReplicas(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	setLabels(t, a, a.secret, nodeID, `{"labels":{"tier":"prod"}}`)

	id := createSelectedService(t, a, "picky", `{"tier":"nowhere"}`, 2)

	nodes := replicaNodes(t, a, id, 2)
	for _, node := range nodes {
		if node != "" {
			t.Fatalf("a replica was placed on %s with no node carrying the label", node)
		}
	}
}

func TestABadSelectorOnAServiceIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	body := `{"name":"bad","isolation":"container","image":"alpine:3.20","replicas":1,` +
		`"node_selector":{"Tier":"prod"}}`
	rec := do(t, a, http.MethodPost, "/v1/services", strings.NewReader(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: the same validation the instance route applies",
			rec.Code, http.StatusBadRequest)
	}
}

func TestAServiceEnvReachesEveryReplicaAndNotTheOperator(t *testing.T) {
	a, nodeID := newSealingApp(t)

	body := `{"name":"pool","isolation":"container","image":"alpine:3.20","replicas":2,` +
		`"env":{"DB_PASSWORD":"hunter2-service"}}`
	rec := do(t, a, http.MethodPost, "/v1/services", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hunter2-service") {
		t.Fatal("the create response echoed the value back")
	}

	var created struct {
		ID       string   `json:"id"`
		EnvNames []string `json:"env_names"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Join(created.EnvNames, ",") != "DB_PASSWORD" {
		t.Fatalf("env_names = %v, want the name and nothing else", created.EnvNames)
	}

	if nodes := replicaNodes(t, a, created.ID, 2); len(nodes) < 2 {
		t.Fatalf("the service never reached two replicas: %v", nodes)
	}

	list := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/instances", nil)
	if !strings.Contains(list.Body.String(), "hunter2-service") {
		t.Fatalf("no replica carries the value, so the template dropped it: %s",
			list.Body.String())
	}
}

func TestAServiceEnvIsRefusedWithNoKeyToSealItWith(t *testing.T) {
	a, _ := newBalancingApp(t)

	body := `{"name":"pool","isolation":"container","image":"alpine:3.20","replicas":1,` +
		`"env":{"TOKEN":"hunter2"}}`
	rec := do(t, a, http.MethodPost, "/v1/services", strings.NewReader(body))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: every replica would carry it", rec.Code,
			http.StatusConflict)
	}
}

func TestAServiceValueIsNotOnDiskInTheClear(t *testing.T) {
	a, dir := sealingAppIn(t, t.TempDir())
	registerNode(t, a, "bm-1", "rack-a")

	body := `{"name":"pool","isolation":"container","image":"alpine:3.20","replicas":1,` +
		`"env":{"TOKEN":"zz4-service-marker"}}`
	if rec := do(t, a, http.MethodPost, "/v1/services", strings.NewReader(body)); rec.Code !=
		http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	onDisk := everythingUnder(t, dir)
	if strings.Contains(onDisk, "zz4-service-marker") {
		t.Fatal("the value is in the control plane's data directory in the clear")
	}
	if !strings.Contains(onDisk, "TOKEN") {
		t.Fatal("the name is not stored either")
	}
}
