package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func addressesOf(t *testing.T, a *testApp, nodeID, instanceID string) []string {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/network", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node view: %d %s", rec.Code, rec.Body.String())
	}

	var body dualNetworks
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	held := make([]string, 0, 3)
	for _, n := range body.Networks {
		for _, nic := range n.NICs {
			if nic.InstanceID != instanceID {
				continue
			}
			held = append(held, nic.IP)
			if nic.IP6 != "" {
				held = append(held, nic.IP6)
			}
		}
	}
	return held
}

func attachedTo(t *testing.T, a *testApp, instanceID, name string) string {
	t.Helper()

	volume := createIn(t, a, a.secret, "/v1/volumes",
		`{"name":"`+name+`","size_gib":1}`)

	rec := do(t, a, http.MethodPost, "/v1/volumes/"+volume+"/attach",
		strings.NewReader(`{"instance_id":"`+instanceID+`"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
	}
	return volume
}

func TestVolumeWorkLeavesEveryAddressAlone(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	front := newDualNetwork(t, a, "front", "10.120.0.0/16", "fd00:c0:1::/48")
	back := newNetwork(t, a, "back", "10.121.0.0/16")

	id := createVMOnNetworks(t, a, "boxed", front, back)
	waitForNICs(t, a, nodeID, id, 2)

	before := addressesOf(t, a, nodeID, id)
	if len(before) != 3 {
		t.Fatalf("addresses = %v, want two v4 and one v6", before)
	}

	reportRunning(t, a, nodeID, id)
	volume := attachedTo(t, a, id, "work")

	if rec := do(t, a, http.MethodPost, "/v1/volumes/"+volume+"/backups",
		strings.NewReader(`{"name":"copy"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("backup: %d %s", rec.Code, rec.Body.String())
	}

	stopInstance(t, a, id)

	if rec := do(t, a, http.MethodPost, "/v1/volumes/"+volume+"/snapshots",
		strings.NewReader(`{"name":"point"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}

	after := addressesOf(t, a, nodeID, id)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("addresses were %v and are now %v: volume work runs on the volume, and "+
			"nothing about it should touch what an instance is reachable on", before, after)
	}
}

func createVMOnNetworks(t *testing.T, a *testApp, name string, ids ...string) string {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"vm","image":"ubuntu-24.04"` +
		`,"network_id":"` + ids[0] + `"`
	if len(ids) > 1 {
		extra, _ := json.Marshal(ids[1:])
		body += `,"extra_networks":` + string(extra)
	}
	body += `}`

	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func reportRunning(t *testing.T, a *testApp, nodeID, instanceID string) {
	t.Helper()

	if placed := waitForPlacement(t, a, instanceID); placed == "" {
		t.Fatalf("instance %s was never placed", instanceID)
	}

	rec := do(t, a, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/status",
		strings.NewReader(`{"observed_state":"running"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("report running: %d %s", rec.Code, rec.Body.String())
	}
}
