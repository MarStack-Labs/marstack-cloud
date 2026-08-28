package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type usageBody struct {
	Nodes []struct {
		NodeID     string  `json:"node_id"`
		CPUPercent float64 `json:"cpu_percent"`
	} `json:"nodes"`
	Instances []struct {
		InstanceID string  `json:"instance_id"`
		CPUPercent float64 `json:"cpu_percent"`
	} `json:"instances"`
}

func reportUsage(t *testing.T, a *testApp, nodeID, body string) {
	t.Helper()

	rec := do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/usage", strings.NewReader(body))
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("report usage: %d %s", rec.Code, rec.Body.String())
	}
}

func readUsage(t *testing.T, a *testApp, secret, path string) usageBody {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read %s: %d %s", path, rec.Code, rec.Body.String())
	}

	var body usageBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestUsageDoesNotReachAcrossProjects(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	mine := runningInstance(t, a, a.secret, "secret-workload", nodeID)
	reportUsage(t, a, nodeID, `{"cpu_percent":42,"memory_used_mib":1000,"memory_mib":6144,
		"instances":[{"instance_id":"`+mine+`","cpu_percent":88,"memory_used_mib":500}]}`)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	seen := readUsage(t, a, other, "/v1/usage")

	for _, in := range seen.Instances {
		if in.InstanceID == mine {
			t.Fatalf("an outsider sees instance %s at %.0f%% cpu, which tells them when "+
				"somebody else's workload is busy", in.InstanceID, in.CPUPercent)
		}
	}
	if len(seen.Nodes) != 0 {
		t.Fatalf("an outsider sees %d nodes, which is infrastructure topology",
			len(seen.Nodes))
	}
}

func TestUsageStillShowsYourOwnInstances(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	mine := runningInstance(t, a, a.secret, "web-1", nodeID)
	reportUsage(t, a, nodeID, `{"cpu_percent":42,"memory_used_mib":1000,"memory_mib":6144,
		"instances":[{"instance_id":"`+mine+`","cpu_percent":88,"memory_used_mib":500}]}`)

	seen := readUsage(t, a, a.secret, "/v1/usage")
	if len(seen.Instances) != 1 || seen.Instances[0].InstanceID != mine {
		t.Fatalf("instances = %+v, want the caller's own workload", seen.Instances)
	}
	if seen.Instances[0].CPUPercent != 88 {
		t.Fatalf("cpu = %v, want 88", seen.Instances[0].CPUPercent)
	}
}

func TestNodeUsageIsAdministrative(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	reportUsage(t, a, nodeID,
		`{"cpu_percent":42,"memory_used_mib":1000,"memory_mib":6144}`)

	admin := readUsage(t, a, a.secret, "/v1/usage/nodes")
	if len(admin.Nodes) != 1 || admin.Nodes[0].CPUPercent != 42 {
		t.Fatalf("nodes = %+v, want an admin to see node load", admin.Nodes)
	}

	member := tokenIn(t, a, "worker", "prj-default")
	if rec := doAs(t, a, member, http.MethodGet, "/v1/usage/nodes",
		nil); rec.Code != http.StatusForbidden {
		t.Fatalf("member = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAViewerSeesItsOwnUsageOnly(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	mine := runningInstance(t, a, a.secret, "web-1", nodeID)
	reportUsage(t, a, nodeID, `{"cpu_percent":42,"memory_used_mib":1000,"memory_mib":6144,
		"instances":[{"instance_id":"`+mine+`","cpu_percent":88,"memory_used_mib":500}]}`)

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

	seen := readUsage(t, a, created.Secret, "/v1/usage")
	if len(seen.Instances) != 1 {
		t.Fatalf("instances = %d, want the viewer to see its project's workload",
			len(seen.Instances))
	}
	if rec := doAs(t, a, created.Secret, http.MethodGet, "/v1/usage/nodes",
		nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer node usage = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
