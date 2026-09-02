package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

type migrateBody struct {
	ID         string `json:"id"`
	Desired    string `json:"desired_state"`
	NodeID     string `json:"node_id"`
	Migrating  bool   `json:"migrating"`
	DiskParked bool   `json:"disk_parked"`
}

func readMigrating(t *testing.T, a *testApp, id string) migrateBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/instances/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read instance: %d %s", rec.Code, rec.Body.String())
	}

	var body migrateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func parkDisk(t *testing.T, a *testApp, nodeID, id, content string) int {
	t.Helper()

	rec := do(t, a, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+id+"/disk", strings.NewReader(content))
	return rec.Code
}

func takeDisk(t *testing.T, a *testApp, nodeID, id string) (int, string) {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/instances/"+id+"/disk", nil)
	return rec.Code, rec.Body.String()
}

func migratingVM(t *testing.T, a *testApp, nodeID string) string {
	t.Helper()

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "mv", "10.220.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	if rec := do(t, a, http.MethodPost, "/v1/nodes/"+nodeID+"/drain", nil); rec.Code !=
		http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("drain: %d %s", rec.Code, rec.Body.String())
	}

	for range 300 {
		if readMigrating(t, a, id).Migrating {
			return id
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("draining never started moving %s", id)
	return ""
}

func steadyMigration(t *testing.T) (*testApp, string, string) {
	t.Helper()

	a := newTestApp(t)
	nodeID := registerNode(t, a, "bm-1", "rack-a")

	made := do(t, a, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"db","isolation":"vm","image":"ubuntu-24.04"}`))
	if made.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", made.Code, made.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(made.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	ctx := t.Context()
	if err := a.instances.Assign(ctx, created.ID, nodeID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := a.instances.BeginMigration(ctx, created.ID, nodeID); err != nil {
		t.Fatalf("begin migration: %v", err)
	}
	return a, created.ID, nodeID
}

func TestStartingAMoveStopsTheWorkloadFirst(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := migratingVM(t, a, nodeID)

	held := readMigrating(t, a, id)
	if !held.Migrating {
		t.Fatal("the instance is not marked as moving")
	}
	if held.Desired != "stopped" {
		t.Fatalf("desired = %q, want stopped: a disk copied out from under a running "+
			"guest is a disk copied mid-write", held.Desired)
	}
	if held.DiskParked {
		t.Fatal("the disk is marked as waiting before anything was handed over")
	}
}

func TestADiskIsCarriedAcross(t *testing.T) {
	a, id, nodeID := steadyMigration(t)

	if code := parkDisk(t, a, nodeID, id, "the guest's disk"); code !=
		http.StatusNoContent {
		t.Fatalf("park: %d", code)
	}
	if !readMigrating(t, a, id).DiskParked {
		t.Fatal("the disk was handed over and nothing says so, so no node will fetch it")
	}

	second := registerNode(t, a, "bm-2", "rack-b")
	ctx := t.Context()
	if err := a.instances.ReleasePlacement(ctx, id, nodeID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := a.instances.Assign(ctx, id, second); err != nil {
		t.Fatalf("assign to the second node: %v", err)
	}

	code, content := takeDisk(t, a, second, id)
	if code != http.StatusOK {
		t.Fatalf("take: %d %s", code, content)
	}
	if content != "the guest's disk" {
		t.Fatalf("content = %q, want the bytes that were handed over", content)
	}
}

func TestTheNodeThatHandedADiskOverCannotTakeItBack(t *testing.T) {
	a, id, nodeID := steadyMigration(t)
	parkDisk(t, a, nodeID, id, "the guest's disk")

	code, _ := takeDisk(t, a, nodeID, id)
	if code == http.StatusOK {
		t.Fatal("the node that handed the disk over took it straight back. It is still the " +
			"assigned node until the scheduler releases the placement, so it wins that " +
			"race every time and the move ends where it started, on the node being drained")
	}
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", code, http.StatusConflict)
	}
}

func TestTheMoveOnlyEndsWhenTheDiskHasLanded(t *testing.T) {
	a, id, nodeID := steadyMigration(t)
	parkDisk(t, a, nodeID, id, "the guest's disk")

	rec := do(t, a, http.MethodPost, "/v1/nodes/"+nodeID+"/instances/"+id+"/landed", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("landed: %d %s", rec.Code, rec.Body.String())
	}

	held := readMigrating(t, a, id)
	if held.Migrating || held.DiskParked {
		t.Fatalf("held = %+v, want the move finished", held)
	}
	if held.Desired != "running" {
		t.Fatalf("desired = %q, want running only now: bringing it back before the disk "+
			"is in place boots a blank overlay, and the move quietly becomes a rebuild",
			held.Desired)
	}

	if code, _ := takeDisk(t, a, nodeID, id); code == http.StatusOK {
		t.Fatal("the parked copy is still there. Two nodes holding one disk is how a " +
			"guest gets started twice from the same state")
	}
}

func TestADrainDoesNotFinishWhileADiskIsStillOnTheNode(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := migratingVM(t, a, nodeID)

	if code := parkDisk(t, a, nodeID, id, "the guest's disk"); code !=
		http.StatusNoContent {
		t.Fatalf("park: %d", code)
	}

	for range 200 {
		if readMigrating(t, a, id).NodeID == "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID, nil)
	t.Fatalf("the instance is still on %s and the node says %s. Stopping it for the move "+
		"takes it out of the list of what a node is still running, so the drain sees an "+
		"empty node and reports itself finished with a disk still on it - and then never "+
		"looks again", nodeID, rec.Body.String())
}

func TestAWorkloadBeingMovedIsStillPlacedSomewhere(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := migratingVM(t, a, nodeID)

	second := registerNode(t, a, "bm-2", "rack-b")
	if code := parkDisk(t, a, nodeID, id, "the guest's disk"); code !=
		http.StatusNoContent {
		t.Fatalf("park: %d", code)
	}

	for range 400 {
		if readMigrating(t, a, id).NodeID == second {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	held := readMigrating(t, a, id)
	t.Fatalf("held = %+v, want it placed on %s. Stopping it for the move also takes it "+
		"out of the list of things waiting for a node, so the disk is parked, the "+
		"placement is released, and then nothing ever picks it up: it sits with no node "+
		"and a disk in the control plane forever", held, second)
}

func TestADiskCannotBeParkedForSomethingThatIsNotMoving(t *testing.T) {
	a, nodeID := newBalancingApp(t)

	id := createVMOnNetworks(t, a, "db", newNetwork(t, a, "mv", "10.221.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)

	if code := parkDisk(t, a, nodeID, id, "sneaky"); code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: a disk arriving for a workload nobody is moving "+
			"has no owner and no reason", code, http.StatusConflict)
	}
}

func TestAnotherNodeCannotParkOrTakeADisk(t *testing.T) {
	a, id, nodeID := steadyMigration(t)

	if code := parkDisk(t, a, "n-somewhere-else", id, "forged"); code == http.StatusNoContent {
		t.Fatal("any node could hand over a disk for an instance it does not hold")
	}

	parkDisk(t, a, nodeID, id, "the guest's disk")
	if code, _ := takeDisk(t, a, "n-somewhere-else", id); code == http.StatusOK {
		t.Fatal("any node could fetch the disk of an instance it was never given")
	}
}

func TestThereIsNothingToTakeBeforeADiskIsParked(t *testing.T) {
	a, id, nodeID := steadyMigration(t)

	if code, _ := takeDisk(t, a, nodeID, id); code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", code, http.StatusNotFound)
	}
}

func TestFurtherDrainPassesDoNotForgetAParkedDisk(t *testing.T) {
	a, id, nodeID := steadyMigration(t)
	parkDisk(t, a, nodeID, id, "the guest's disk")

	for range 50 {
		time.Sleep(2 * time.Millisecond)
	}

	if !readMigrating(t, a, id).DiskParked {
		t.Fatal("starting the move again forgot that the disk was already handed over, " +
			"so it would be copied a second time or waited for forever")
	}
}
