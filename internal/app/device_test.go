package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type deviceBody struct {
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	Kind       string `json:"kind"`
	Driver     string `json:"driver"`
	Ready      bool   `json:"ready"`
	InstanceID string `json:"instance_id"`
}

func waitForNode(t *testing.T, a *testApp, id string) placedBody {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var last placedBody

	for time.Now().Before(deadline) {
		last = readPlaced(t, a, id)
		if last.NodeID != "" {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

func readPlaced(t *testing.T, a *testApp, id string) placedBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/instances/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read instance: %d %s", rec.Code, rec.Body.String())
	}

	var held placedBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return held
}

func reportDevices(t *testing.T, a *testApp, nodeID, body string) {
	t.Helper()

	rec := do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/devices", strings.NewReader(body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report devices: %d %s", rec.Code, rec.Body.String())
	}
}

func readDevices(t *testing.T, a *testApp) []deviceBody {
	t.Helper()

	rec := doAs(t, a, a.secret, http.MethodGet, "/v1/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read devices: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Devices []deviceBody `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Devices
}

func vmWantingADevice(t *testing.T, a *testApp, name, networkID, kind string) (int, string) {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"vm","image":"ubuntu-24.04",` +
		`"network_id":"` + networkID + `","device":"` + kind + `"}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))

	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	return rec.Code, created.ID
}

func TestAWorkloadWaitsUntilANodeHasAFreeDevice(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "gpunet", "10.240.0.0/16")

	code, id := vmWantingADevice(t, a, "trainer", network, "gpu")
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}

	held := placement(t, a, id)
	if held.NodeID != "" {
		t.Fatalf("node = %q, want it held: placing a workload that asked for a gpu on a "+
			"machine without one is worse than making it wait", held.NodeID)
	}
	if !strings.Contains(held.Message, "gpu") {
		t.Fatalf("message = %q, want it to say what is missing", held.Message)
	}

	reportDevices(t, a, nodeID, `{"devices":[
		{"address":"0000:01:00.0","kind":"gpu","driver":"vfio-pci","ready":true}]}`)

	held = waitForNode(t, a, id)
	if held.NodeID == "" {
		t.Fatal("the workload was still held after a node reported a free gpu")
	}

	for _, one := range readDevices(t, a) {
		if one.Address == "0000:01:00.0" && one.InstanceID != id {
			t.Fatalf("device = %+v, want it claimed by %s: two workloads given one card "+
				"both try to open it and the second one fails to boot", one, id)
		}
	}
}

func TestADeviceTheHostStillHoldsIsNotOffered(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "gpunet", "10.241.0.0/16")

	reportDevices(t, a, nodeID, `{"devices":[
		{"address":"0000:01:00.0","kind":"gpu","driver":"nvidia","ready":false}]}`)

	_, id := vmWantingADevice(t, a, "trainer", network, "gpu")

	if held := placement(t, a, id); held.NodeID != "" {
		t.Fatal("a workload was placed on a card the host driver still holds. qemu cannot " +
			"open it, so the guest fails to boot after the placement already happened")
	}
}

func TestOneCardGoesToOneWorkload(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "gpunet", "10.242.0.0/16")

	reportDevices(t, a, nodeID, `{"devices":[
		{"address":"0000:01:00.0","kind":"gpu","driver":"vfio-pci","ready":true}]}`)

	_, first := vmWantingADevice(t, a, "first", network, "gpu")
	placement(t, a, first)
	_, second := vmWantingADevice(t, a, "second", network, "gpu")
	placement(t, a, second)

	placed := 0
	for _, id := range []string{first, second} {
		if readPlaced(t, a, id).NodeID != "" {
			placed++
		}
	}
	if placed != 1 {
		t.Fatalf("%d workloads were placed on one card, want one", placed)
	}

	waiting := readPlaced(t, a, second)
	if readPlaced(t, a, first).NodeID == "" {
		waiting = readPlaced(t, a, first)
	}
	if !strings.Contains(waiting.Message, "gpu") {
		t.Fatalf("message = %q, want it to say there is no free gpu. A workload the "+
			"scheduler keeps skipping in silence looks exactly like a broken scheduler",
			waiting.Message)
	}
}

func TestADeletedWorkloadGivesItsCardBack(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "gpunet", "10.243.0.0/16")

	reportDevices(t, a, nodeID, `{"devices":[
		{"address":"0000:01:00.0","kind":"gpu","driver":"vfio-pci","ready":true}]}`)

	_, first := vmWantingADevice(t, a, "first", network, "gpu")
	waitForNode(t, a, first)

	if rec := do(t, a, http.MethodDelete, "/v1/instances/"+first, nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	for _, one := range readDevices(t, a) {
		if one.Address == "0000:01:00.0" && one.InstanceID != "" {
			t.Fatalf("device = %+v, still claimed by a workload that is gone. The card is "+
				"lost to the fleet until the node restarts", one)
		}
	}
}

func TestANodeReportingAgainKeepsTheClaim(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "gpunet", "10.244.0.0/16")

	reportDevices(t, a, nodeID, `{"devices":[
		{"address":"0000:01:00.0","kind":"gpu","driver":"vfio-pci","ready":true}]}`)

	_, id := vmWantingADevice(t, a, "trainer", network, "gpu")
	waitForNode(t, a, id)

	reportDevices(t, a, nodeID, `{"devices":[
		{"address":"0000:01:00.0","kind":"gpu","driver":"vfio-pci","ready":true}]}`)

	for _, one := range readDevices(t, a) {
		if one.Address != "0000:01:00.0" {
			continue
		}
		if one.InstanceID != id {
			t.Fatalf("device = %+v, want it still claimed. The node reports its inventory "+
				"every pass, and losing the claim each time hands the same card out twice",
				one)
		}
		return
	}
	t.Fatal("the device vanished from the inventory")
}

func TestOnlyAVmCanBeGivenADevice(t *testing.T) {
	a, _ := newBalancingApp(t)

	body := `{"name":"boxed","isolation":"container","image":"alpine:3.20","device":"gpu"}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a device is handed over through vfio, which needs a "+
			"guest kernel", rec.Code, http.StatusBadRequest)
	}
}

func TestANodeSeesTheAddressItWasGiven(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	network := newNetwork(t, a, "gpunet", "10.245.0.0/16")

	reportDevices(t, a, nodeID, `{"devices":[
		{"address":"0000:81:00.0","kind":"gpu","driver":"vfio-pci","ready":true}]}`)

	_, id := vmWantingADevice(t, a, "trainer", network, "gpu")
	waitForNode(t, a, id)

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/instances", nil)
	var body struct {
		Instances []struct {
			ID            string `json:"id"`
			DeviceAddress string `json:"device_address"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, one := range body.Instances {
		if one.ID != id {
			continue
		}
		if one.DeviceAddress != "0000:81:00.0" {
			t.Fatalf("address = %q, want the one it was given. Without it the node has "+
				"nothing to put on the qemu command line", one.DeviceAddress)
		}
		return
	}
	t.Fatalf("the instance is not in the node view: %s", rec.Body.String())
}
