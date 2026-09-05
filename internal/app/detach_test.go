package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type detachBody struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	InstanceID string `json:"instance_id"`
	Detaching  bool   `json:"detaching"`
}

func detachVolume(t *testing.T, a *testApp, id string) detachBody {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/volumes/"+id+"/detach", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detach: %d %s", rec.Code, rec.Body.String())
	}

	var held detachBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return held
}

func readVolume(t *testing.T, a *testApp, id string) detachBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/volumes/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read volume: %d %s", rec.Code, rec.Body.String())
	}

	var held detachBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return held
}

func TestDetachingARunningGuestsDiskWaitsForTheGuest(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "dsk", "10.220.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	volume := attachedTo(t, a, id, "data")

	held := detachVolume(t, a, volume)
	if held.State != "detaching" {
		t.Fatalf("state = %q, want detaching: a disk a guest is writing to cannot be taken "+
			"away by forgetting the row, and pretending it is gone loses whatever was in "+
			"flight", held.State)
	}
	if held.InstanceID != id {
		t.Fatalf("instance = %q, want it still named until the guest lets go", held.InstanceID)
	}
}

func TestANodeReportingTheDiskGoneFinishesTheDetach(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "dsk", "10.221.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	volume := attachedTo(t, a, id, "data")
	detachVolume(t, a, volume)

	rec := do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/volumes",
		strings.NewReader(`{"volumes":[{"volume_id":"`+volume+
			`","detached":true,"snapshots":[]}]}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	held := readVolume(t, a, volume)
	if held.State != "free" || held.InstanceID != "" {
		t.Fatalf("volume = %+v, want it free once the guest let go", held)
	}
}

func TestAGuestThatWillNotLetGoKeepsTheDisk(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "dsk", "10.222.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	volume := attachedTo(t, a, id, "data")
	detachVolume(t, a, volume)

	rec := do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/volumes",
		strings.NewReader(`{"volumes":[{"volume_id":"`+volume+
			`","error":"the guest still holds the disk","snapshots":[]}]}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	held := readVolume(t, a, volume)
	if held.State != "detaching" {
		t.Fatalf("state = %q, want it still detaching: a guest that will not unmount has not "+
			"released anything, and calling it free would let the disk be attached somewhere "+
			"else while it is still being written to", held.State)
	}
}

func TestDetachingFromAStoppedGuestIsImmediate(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "dsk", "10.223.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)

	volume := attachedTo(t, a, id, "data")

	if rec := do(t, a, http.MethodPost, "/v1/instances/"+id+"/stop", nil); rec.Code !=
		http.StatusAccepted {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
	}

	held := detachVolume(t, a, volume)
	if held.State != "free" {
		t.Fatalf("state = %q, want free at once: there is no guest to ask, so waiting for one "+
			"to answer would wait forever", held.State)
	}
}

func TestADetachingDiskLeavesTheNodesWantedSet(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "dsk", "10.224.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	volume := attachedTo(t, a, id, "data")
	detachVolume(t, a, volume)

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/volumes", nil)
	var body struct {
		Volumes []struct {
			ID         string `json:"id"`
			InstanceID string `json:"instance_id"`
			Detaching  bool   `json:"detaching"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, one := range body.Volumes {
		if one.ID != volume {
			continue
		}
		if !one.Detaching {
			t.Fatal("the node is not told the disk is on its way out, so it never asks the " +
				"guest to release it and the volume stays detaching forever")
		}
		if one.InstanceID != id {
			t.Fatalf("instance = %q, want it still named: the node has to know which guest "+
				"to ask", one.InstanceID)
		}
		return
	}
	t.Fatalf("the volume left the node view entirely: %s", rec.Body.String())
}

func TestAttachingItBackCancelsTheDetach(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "dsk", "10.225.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	volume := attachedTo(t, a, id, "data")
	if held := detachVolume(t, a, volume); held.State != "detaching" {
		t.Fatalf("state = %q, want detaching", held.State)
	}

	code, attached := attachRef(t, a, a.secret, volume, id)
	if code != http.StatusOK {
		t.Fatalf("re-attach: %d", code)
	}
	if attached.State != "attached" {
		t.Fatalf("state = %q, want attached: an operator who changes their mind has no other "+
			"way out, and a volume nothing ever answers for would sit in detaching forever",
			attached.State)
	}
}
