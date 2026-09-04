package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type attachedVolumeBody struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	InstanceID string `json:"instance_id"`
	NodeID     string `json:"node_id"`
	State      string `json:"state"`
}

func attachRef(t *testing.T, a *testApp, secret, volumeID, ref string) (int, attachedVolumeBody) {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodPost, "/v1/volumes/"+volumeID+"/attach",
		strings.NewReader(`{"instance_id":"`+ref+`"}`))

	var attached attachedVolumeBody
	_ = json.Unmarshal(rec.Body.Bytes(), &attached)
	return rec.Code, attached
}

func placedVMNamed(t *testing.T, a *testApp, nodeID, name string) string {
	t.Helper()

	id := createVMOnNetworks(t, a, name, newNetwork(t, a, "vol-"+name, "10.240.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)
	return id
}

func TestAVolumeAttachesToAnInstanceByName(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := placedVMNamed(t, a, nodeID, "db")

	volumeID := createIn(t, a, a.secret, "/v1/volumes", `{"name":"data","size_gib":1}`)

	code, attached := attachRef(t, a, a.secret, volumeID, "db")
	if code != http.StatusOK {
		t.Fatalf("attach: %d", code)
	}
	if attached.InstanceID != id {
		t.Fatalf("instance = %q, want %q: the volume module does its own lookup, and the "+
			"name it was handed must never be what gets stored", attached.InstanceID, id)
	}

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/volumes", nil)
	var view struct {
		Volumes []attachedVolumeBody `json:"volumes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, one := range view.Volumes {
		if one.ID != volumeID {
			continue
		}
		if one.InstanceID != id {
			t.Fatalf("the node is told to attach %q, and it matches its disks by id, so "+
				"nothing would ever happen", one.InstanceID)
		}
		return
	}
	t.Fatalf("the volume is not in the node's view: %s", rec.Body.String())
}

func TestAnInstanceNameOnlyResolvesInsideTheProject(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	placedVMNamed(t, a, nodeID, "db")

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)
	volumeID := createIn(t, a, theirs, "/v1/volumes", `{"name":"theirs","size_gib":1}`)

	if code, _ := attachRef(t, a, theirs, volumeID, "db"); code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: a name that exists in another project must answer "+
			"the way one that does not exist answers, or a disk crosses a tenant boundary "+
			"because somebody picked a common name", code, http.StatusNotFound)
	}
}

func TestAnAttachedVolumeIsStillDetachableAfterBeingNamed(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	placedVMNamed(t, a, nodeID, "db")

	volumeID := createIn(t, a, a.secret, "/v1/volumes", `{"name":"data","size_gib":1}`)
	if code, _ := attachRef(t, a, a.secret, volumeID, "db"); code != http.StatusOK {
		t.Fatalf("attach: %d", code)
	}

	rec := do(t, a, http.MethodPost, "/v1/volumes/"+volumeID+"/detach", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detach: %d %s", rec.Code, rec.Body.String())
	}

	var held attachedVolumeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if held.InstanceID != "" {
		t.Fatalf("instance = %q, want it released", held.InstanceID)
	}
}
