package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type nicView struct {
	InstanceID string `json:"instance_id"`
	Device     int    `json:"device"`
	IP         string `json:"ip"`
	MAC        string `json:"mac"`
}

type nodeNetworksBody struct {
	Networks []struct {
		NetworkID string    `json:"network_id"`
		Name      string    `json:"name"`
		NICs      []nicView `json:"nics"`
	} `json:"networks"`
}

func newNetwork(t *testing.T, a *testApp, name, cidr string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/networks",
		strings.NewReader(`{"name":"`+name+`","cidr":"`+cidr+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create network %s: %d %s", name, rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func nicsOf(t *testing.T, a *testApp, nodeID, instanceID string) []nicView {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/network", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node view: %d %s", rec.Code, rec.Body.String())
	}

	var body nodeNetworksBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	mine := make([]nicView, 0, 2)
	for _, n := range body.Networks {
		for _, nic := range n.NICs {
			if nic.InstanceID == instanceID {
				mine = append(mine, nic)
			}
		}
	}
	return mine
}

func createOnNetworks(t *testing.T, a *testApp, name string, ids ...string) string {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"container","image":"alpine:3.20"`
	if len(ids) > 0 {
		body += `,"network_id":"` + ids[0] + `"`
	}
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

func waitForNICs(t *testing.T, a *testApp, nodeID, instanceID string, want int) []nicView {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var last []nicView

	for time.Now().Before(deadline) {
		last = nicsOf(t, a, nodeID, instanceID)
		if len(last) >= want {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

func TestAnInstanceGetsOneAddressPerNetworkItAsksFor(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	front := newNetwork(t, a, "front", "10.60.0.0/16")
	back := newNetwork(t, a, "back", "10.61.0.0/16")

	id := createOnNetworks(t, a, "dual", front, back)
	nics := waitForNICs(t, a, nodeID, id, 2)

	if len(nics) != 2 {
		t.Fatalf("nics = %d, want one per network asked for", len(nics))
	}

	devices := map[int]string{}
	for _, nic := range nics {
		devices[nic.Device] = nic.IP
	}
	if _, has := devices[0]; !has {
		t.Fatalf("nics = %+v, want a device 0", nics)
	}
	if _, has := devices[1]; !has {
		t.Fatalf("nics = %+v, want a device 1", nics)
	}
	if devices[0] == devices[1] {
		t.Fatal("both interfaces got the same address")
	}
	if !strings.HasPrefix(devices[0], "10.60.") || !strings.HasPrefix(devices[1], "10.61.") {
		t.Fatalf("addresses = %v, want each one out of the network it was asked for", devices)
	}
}

func TestTheMACsDiffer(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	front := newNetwork(t, a, "front", "10.62.0.0/16")
	back := newNetwork(t, a, "back", "10.63.0.0/16")

	id := createOnNetworks(t, a, "dual", front, back)
	nics := waitForNICs(t, a, nodeID, id, 2)

	if len(nics) != 2 {
		t.Fatalf("nics = %d", len(nics))
	}
	if nics[0].MAC == nics[1].MAC {
		t.Fatal("both interfaces got the same mac, which is a bridge loop waiting to happen")
	}
}

func TestOneNetworkStillGetsExactlyOneNIC(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	only := newNetwork(t, a, "only", "10.64.0.0/16")
	id := createOnNetworks(t, a, "single", only)

	nics := waitForNICs(t, a, nodeID, id, 1)
	if len(nics) != 1 {
		t.Fatalf("nics = %d, want the single-network case untouched", len(nics))
	}
	if nics[0].Device != 0 {
		t.Fatalf("device = %d, want 0", nics[0].Device)
	}
}

func TestNamingTheSameNetworkTwiceIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)
	only := newNetwork(t, a, "only", "10.65.0.0/16")

	body := `{"name":"silly","isolation":"container","image":"alpine:3.20",` +
		`"network_id":"` + only + `","extra_networks":["` + only + `"]}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: two interfaces onto one network would carry two "+
			"addresses out of one slice", rec.Code, http.StatusBadRequest)
	}
}

func TestTooManyNetworksIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	extra := make([]string, 0, 4)
	for i := range 4 {
		extra = append(extra, newNetwork(t, a,
			"extra-"+string(rune('a'+i)), "10.7"+string(rune('0'+i))+".0.0/16"))
	}

	encoded, _ := json.Marshal(extra)
	body := `{"name":"greedy","isolation":"container","image":"alpine:3.20",` +
		`"extra_networks":` + string(encoded) + `}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDeletingAnInstanceReleasesEveryAddress(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	front := newNetwork(t, a, "front", "10.66.0.0/16")
	back := newNetwork(t, a, "back", "10.67.0.0/16")

	id := createOnNetworks(t, a, "dual", front, back)
	if nics := waitForNICs(t, a, nodeID, id, 2); len(nics) != 2 {
		t.Fatalf("nics = %d before delete", len(nics))
	}

	if rec := do(t, a, http.MethodDelete, "/v1/instances/"+id, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if nics := nicsOf(t, a, nodeID, id); len(nics) != 0 {
		t.Fatalf("nics = %+v after delete, want every address released", nics)
	}
}
