package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type cloneBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SizeGiB   int    `json:"size_gib"`
	NodeID    string `json:"node_id"`
	CloneFrom string `json:"clone_from"`
	CloneSnap string `json:"clone_snap"`
	Encrypted bool   `json:"encrypted"`
}

func snapshotOf(t *testing.T, a *testApp, volumeID, name string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/volumes/"+volumeID+"/snapshots",
		strings.NewReader(`{"name":"`+name+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func readySnapshot(t *testing.T, a *testApp, nodeID, volumeID, name string) string {
	t.Helper()

	id := snapshotOf(t, a, volumeID, name)

	body, err := json.Marshal(map[string]any{
		"volumes": []map[string]any{{
			"volume_id": volumeID,
			"snapshots": []map[string]any{{"name": name, "size_bytes": 4096}},
		}},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	rec := do(t, a, http.MethodPut, "/v1/nodes/"+nodeID+"/volumes",
		strings.NewReader(string(body)))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("report snapshots: %d %s", rec.Code, rec.Body.String())
	}
	return id
}

func clonedFrom(t *testing.T, a *testApp, snapshotID, name string) (int, cloneBody) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/snapshots/"+snapshotID+"/clone",
		strings.NewReader(`{"name":"`+name+`"}`))

	var made cloneBody
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	return rec.Code, made
}

func placedVolume(t *testing.T, a *testApp, nodeID, name string) (string, string) {
	t.Helper()

	id := createVMOnNetworks(t, a, "holder-"+name, newNetwork(t, a, "n-"+name,
		"10.210.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	volume := attachedTo(t, a, id, name)
	stopInstance(t, a, id)
	return volume, id
}

func TestASnapshotBecomesAVolumeOfItsOwn(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	source, _ := placedVolume(t, a, nodeID, "source")
	snapshot := readySnapshot(t, a, nodeID, source, "point")

	code, made := clonedFrom(t, a, snapshot, "copy")
	if code != http.StatusCreated {
		t.Fatalf("clone: %d", code)
	}
	if made.CloneFrom != source || made.CloneSnap != "point" {
		t.Fatalf("clone = %+v, want it to say what it came from", made)
	}
	if made.NodeID != nodeID {
		t.Fatalf("node = %q, want %q: a snapshot lives inside the disk file on one node, "+
			"so the copy has to be made there", made.NodeID, nodeID)
	}

	rec := do(t, a, http.MethodGet, "/v1/volumes/"+source, nil)
	var before struct {
		SizeGiB int `json:"size_gib"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &before)
	if made.SizeGiB != before.SizeGiB {
		t.Fatalf("size = %d, want the source's %d", made.SizeGiB, before.SizeGiB)
	}
}

func TestTheSourceVolumeIsUntouched(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	source, _ := placedVolume(t, a, nodeID, "source")
	snapshot := readySnapshot(t, a, nodeID, source, "point")

	if code, _ := clonedFrom(t, a, snapshot, "copy"); code != http.StatusCreated {
		t.Fatalf("clone: %d", code)
	}

	rec := do(t, a, http.MethodGet, "/v1/volumes/"+source, nil)
	var held struct {
		CloneFrom   string `json:"clone_from"`
		RestoreFrom string `json:"restore_from"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &held)

	if held.CloneFrom != "" || held.RestoreFrom != "" {
		t.Fatalf("the source now says clone_from=%q restore_from=%q. Copying is not "+
			"restoring: the point of it is that the original is left alone",
			held.CloneFrom, held.RestoreFrom)
	}
}

func TestASnapshotThatIsNotReadyCannotBeCopied(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	source, _ := placedVolume(t, a, nodeID, "source")
	pending := snapshotOf(t, a, source, "point")

	if code, _ := clonedFrom(t, a, pending, "copy"); code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: there is nothing on disk to copy yet", code,
			http.StatusConflict)
	}
}

func TestAVolumeThatIsOnNoNodeHasNothingToCopy(t *testing.T) {
	a, _ := newBalancingApp(t)

	volume := createIn(t, a, a.secret, "/v1/volumes", `{"name":"loose","size_gib":1}`)
	rec := do(t, a, http.MethodPost, "/v1/volumes/"+volume+"/snapshots",
		strings.NewReader(`{"name":"point"}`))

	if rec.Code == http.StatusCreated {
		var snap struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &snap)
		if code, _ := clonedFrom(t, a, snap.ID, "copy"); code == http.StatusCreated {
			t.Fatal("a volume with no node was copied. The snapshot is inside a disk file " +
				"on a node, and there is no file")
		}
		return
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("snapshot of an unplaced volume: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnEncryptedVolumeIsNotCopiedQuietly(t *testing.T) {
	a, nodeID := newSealingApp(t)

	id := createVMOnNetworks(t, a, "holder", newNetwork(t, a, "enc", "10.211.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	reportRunning(t, a, nodeID, id)

	made := do(t, a, http.MethodPost, "/v1/volumes",
		strings.NewReader(`{"name":"secret","size_gib":1,"encrypted":true}`))
	if made.Code != http.StatusCreated {
		t.Fatalf("create an encrypted volume: %d %s", made.Code, made.Body.String())
	}
	var enc struct {
		ID        string `json:"id"`
		Encrypted bool   `json:"encrypted"`
	}
	_ = json.Unmarshal(made.Body.Bytes(), &enc)
	if !enc.Encrypted {
		t.Fatal("the volume was made but is not encrypted, so this proves nothing")
	}

	if rec := do(t, a, http.MethodPost, "/v1/volumes/"+enc.ID+"/attach",
		strings.NewReader(`{"instance_id":"`+id+`"}`)); rec.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
	}
	stopInstance(t, a, id)

	snapshot := readySnapshot(t, a, nodeID, enc.ID, "point")
	code, _ := clonedFrom(t, a, snapshot, "copy")

	if code == http.StatusCreated {
		t.Fatal("an encrypted volume was copied. Either the copy needs the key on a second " +
			"disk or its contents are written out in the clear, and neither is something " +
			"to do without being asked")
	}
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", code, http.StatusConflict)
	}
}

func TestACopyCountsAgainstTheQuota(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	source, _ := placedVolume(t, a, nodeID, "source")
	snapshot := readySnapshot(t, a, nodeID, source, "point")

	setQuota(t, a, "prj-default", `{"volumes":1}`)

	if code, _ := clonedFrom(t, a, snapshot, "copy"); code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: a copy is a whole volume and has to be paid for "+
			"like one", code, http.StatusConflict)
	}
}

func TestAnotherProjectsSnapshotCannotBeCopied(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	source, _ := placedVolume(t, a, nodeID, "source")
	snapshot := readySnapshot(t, a, nodeID, source, "point")

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodPost, "/v1/snapshots/"+snapshot+"/clone",
		strings.NewReader(`{"name":"copy"}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestTheNodeIsToldWhatToCopy(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	source, _ := placedVolume(t, a, nodeID, "source")
	snapshot := readySnapshot(t, a, nodeID, source, "point")

	if code, _ := clonedFrom(t, a, snapshot, "copy"); code != http.StatusCreated {
		t.Fatalf("clone: %d", code)
	}

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/volumes", nil)
	var view struct {
		Volumes []cloneBody `json:"volumes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, one := range view.Volumes {
		if one.CloneFrom == source && one.CloneSnap == "point" {
			return
		}
	}
	t.Fatalf("the node's view does not carry what to copy, so nothing will ever happen: %s",
		rec.Body.String())
}
