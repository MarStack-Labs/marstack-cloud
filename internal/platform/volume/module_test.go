package volume

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type fakeInstances struct {
	placements map[string]Placement
}

func (f fakeInstances) Placement(_ context.Context, instanceID string) (Placement, error) {
	placed, known := f.placements[instanceID]
	if !known {
		return Placement{}, fault.NotFound("instance_not_found", "no instance with that id exists")
	}
	return placed, nil
}

func newTestModule(t *testing.T, instances Instances) (http.Handler, *Module) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, instances, logging.New("error", io.Discard))
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mux := http.NewServeMux()
	m.Routes(mux)
	return mux, m
}

func request(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func create(t *testing.T, h http.Handler, body string) response {
	t.Helper()

	rec := request(t, h, http.MethodPost, "/v1/volumes", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func placedVM(nodeID string) fakeInstances {
	return fakeInstances{placements: map[string]Placement{
		"i-1":         {NodeID: nodeID, Isolation: "vm"},
		"i-2":         {NodeID: nodeID, Isolation: "vm"},
		"i-elsewhere": {NodeID: "n-other", Isolation: "vm"},
		"i-container": {NodeID: nodeID, Isolation: "container"},
		"i-unplaced":  {NodeID: "", Isolation: "vm"},
	}}
}

func TestAVolumeIsFreeUntilItIsAttached(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))

	created := create(t, h, `{"name":"data-1","size_gib":20}`)
	if created.State != StateFree {
		t.Fatalf("state = %q, want %q", created.State, StateFree)
	}
	if created.NodeID != "" {
		t.Fatalf("node = %q, want no node before the first attach", created.NodeID)
	}
}

func TestAttachBindsTheVolumeToTheNodeOfTheInstance(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach",
		`{"instance_id":"i-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
	}

	var attached response
	if err := json.Unmarshal(rec.Body.Bytes(), &attached); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if attached.NodeID != "n-1" || attached.InstanceID != "i-1" {
		t.Fatalf("attached = %+v", attached)
	}
	if attached.State != StateAttached {
		t.Fatalf("state = %q", attached.State)
	}
}

func TestAttachRefusesWhatCannotWork(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)

	cases := map[string]struct {
		body string
		want int
	}{
		"an unplaced instance": {`{"instance_id":"i-unplaced"}`, http.StatusConflict},
		"a container":          {`{"instance_id":"i-container"}`, http.StatusBadRequest},
		"an unknown instance":  {`{"instance_id":"i-nope"}`, http.StatusNotFound},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", want.body)
			if rec.Code != want.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, want.want, rec.Body.String())
			}
		})
	}
}

func TestAVolumeTakesOneWriterAtATime(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)

	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-2"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: two instances writing one disk corrupts it",
			rec.Code, http.StatusConflict)
	}

	if rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach",
		`{"instance_id":"i-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want reattaching to the same instance to be a no-op", rec.Code)
	}
}

func TestAVolumeDoesNotFollowAnInstanceToAnotherNode(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)

	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)
	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/detach", "")

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach",
		`{"instance_id":"i-elsewhere"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: the data is on n-1 and nothing copies it",
			rec.Code, http.StatusConflict)
	}
}

func TestDeleteRefusesAnAttachedVolume(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)

	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)

	if rec := request(t, h, http.MethodDelete, "/v1/volumes/data-1", ""); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}

	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/detach", "")

	if rec := request(t, h, http.MethodDelete, "/v1/volumes/data-1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestDeletingAnInstanceFreesItsVolumes(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)

	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)

	if err := m.ReleaseInstance(context.Background(), "i-1"); err != nil {
		t.Fatalf("release: %v", err)
	}

	var after response
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/volumes/data-1", "").Body.Bytes(), &after,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.State != StateFree || after.InstanceID != "" {
		t.Fatalf("volume = %+v, want it free so it can be attached again", after)
	}
	if after.NodeID != "n-1" {
		t.Fatalf("node = %q, want the volume to stay where its data is", after.NodeID)
	}
}

func TestTheNodeOnlySeesItsOwnVolumes(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))

	first := create(t, h, `{"name":"data-1","size_gib":20}`)
	create(t, h, `{"name":"data-2","size_gib":5}`)

	request(t, h, http.MethodPost, "/v1/volumes/"+first.ID+"/attach", `{"instance_id":"i-1"}`)

	var list listResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/nodes/n-1/volumes", "").Body.Bytes(), &list,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Volumes) != 1 || list.Volumes[0].Name != "data-1" {
		t.Fatalf("volumes = %+v, want only the one bound to this node", list.Volumes)
	}

	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/nodes/n-2/volumes", "").Body.Bytes(), &list,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Volumes) != 0 {
		t.Fatalf("volumes = %+v, want none", list.Volumes)
	}
}

func TestBadVolumesAreRejected(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))

	cases := map[string]string{
		"no name":       `{"size_gib":10}`,
		"no size":       `{"name":"data-1"}`,
		"negative size": `{"name":"data-1","size_gib":-5}`,
		"absurd size":   `{"name":"data-1","size_gib":999999}`,
		"unknown field": `{"name":"data-1","size_gib":10,"node":"n-1"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := request(t, h, http.MethodPost, "/v1/volumes", body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func runningVM(nodeID string) fakeInstances {
	placements := placedVM(nodeID)
	for id, placed := range placements.placements {
		placed.Running = true
		placements.placements[id] = placed
	}
	return placements
}

func snapshotOf(t *testing.T, h http.Handler, volumeID, name string) snapshotResponse {
	t.Helper()

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+volumeID+"/snapshots",
		`{"name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}

	var created snapshotResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func TestASnapshotStartsPendingUntilANodeTakesIt(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)
	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)

	snap := snapshotOf(t, h, created.ID, "before-upgrade")
	if snap.State != SnapshotPending {
		t.Fatalf("state = %q, want %q", snap.State, SnapshotPending)
	}

	body := `{"volumes":[{"volume_id":"` + created.ID +
		`","snapshots":[{"name":"before-upgrade","size_bytes":4096}]}]}`
	if rec := request(t, h, http.MethodPut, "/v1/nodes/n-1/volumes", body); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}

	var list snapshotListResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/volumes/data-1/snapshots", "").Body.Bytes(), &list,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Snapshots) != 1 || list.Snapshots[0].State != SnapshotReady {
		t.Fatalf("snapshots = %+v, want the reported one marked ready", list.Snapshots)
	}
	if list.Snapshots[0].SizeBytes != 4096 {
		t.Fatalf("size = %d, want what the node measured", list.Snapshots[0].SizeBytes)
	}
}

func TestSnapshotsNeedTheGuestStopped(t *testing.T) {
	h, _ := newTestModule(t, runningVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)
	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/snapshots",
		`{"name":"live"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: qemu has the image open and it is changing underneath",
			rec.Code, http.StatusConflict)
	}
}

func TestAnUntouchedVolumeHasNothingToSnapshot(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/snapshots", `{"name":"empty"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: no node holds the volume, so no file exists",
			rec.Code, http.StatusConflict)
	}
}

func TestRestoreWaitsForAReadySnapshot(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)
	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)

	snap := snapshotOf(t, h, created.ID, "before-upgrade")

	if rec := request(t, h, http.MethodPost, "/v1/snapshots/"+snap.ID+"/restore", ""); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d while the snapshot is still pending",
			rec.Code, http.StatusConflict)
	}

	request(t, h, http.MethodPut, "/v1/nodes/n-1/volumes",
		`{"volumes":[{"volume_id":"`+created.ID+`","snapshots":[{"name":"before-upgrade"}]}]}`)

	rec := request(t, h, http.MethodPost, "/v1/snapshots/"+snap.ID+"/restore", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}

	var asked response
	if err := json.Unmarshal(rec.Body.Bytes(), &asked); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if asked.RestoreFrom != "before-upgrade" {
		t.Fatalf("restore_from = %q, want the node to be told what to roll back to", asked.RestoreFrom)
	}

	request(t, h, http.MethodPut, "/v1/nodes/n-1/volumes",
		`{"volumes":[{"volume_id":"`+created.ID+`","snapshots":[{"name":"before-upgrade"}],`+
			`"restored":"before-upgrade"}]}`)

	var after response
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/volumes/data-1", "").Body.Bytes(), &after,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.RestoreFrom != "" {
		t.Fatalf("restore_from = %q, want it cleared once the node did it", after.RestoreFrom)
	}
}

func TestAFailedSnapshotSaysWhy(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)
	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)
	snapshotOf(t, h, created.ID, "doomed")

	request(t, h, http.MethodPut, "/v1/nodes/n-1/volumes",
		`{"volumes":[{"volume_id":"`+created.ID+`","snapshots":[],"error":"no space left on device"}]}`)

	var list snapshotListResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/snapshots", "").Body.Bytes(), &list,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Snapshots) != 1 {
		t.Fatalf("snapshots = %d", len(list.Snapshots))
	}
	if list.Snapshots[0].State != SnapshotFailed {
		t.Fatalf("state = %q, want %q", list.Snapshots[0].State, SnapshotFailed)
	}
	if list.Snapshots[0].Message == "" {
		t.Fatal("a failed snapshot with no reason is a snapshot nobody can fix")
	}
}

func TestDeletingAVolumeTakesItsSnapshots(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)
	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)
	snapshotOf(t, h, created.ID, "keeper")

	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/detach", "")
	if rec := request(t, h, http.MethodDelete, "/v1/volumes/data-1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	var list snapshotListResponse
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/snapshots", "").Body.Bytes(), &list,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Snapshots) != 0 {
		t.Fatalf("snapshots = %+v, want none left pointing at a volume that is gone", list.Snapshots)
	}
}

func TestTwoSnapshotsCannotShareAName(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))
	created := create(t, h, `{"name":"data-1","size_gib":20}`)
	request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/attach", `{"instance_id":"i-1"}`)
	snapshotOf(t, h, created.ID, "twice")

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+created.ID+"/snapshots", `{"name":"twice"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestAnEncryptedVolumeNeedsAKeyToProtectItsKeyWith(t *testing.T) {
	h, _ := newTestModule(t, placedVM("n-1"))

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"secret","size_gib":1,"encrypted":true}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: without an operator key the volume key would sit in "+
			"the database in the clear, which only looks like encryption",
			rec.Code, http.StatusServiceUnavailable)
	}
}

func TestAnEncryptedVolumeCarriesASealedKey(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"secret","size_gib":1,"encrypted":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !created.Encrypted || created.KeyID != operator.ID() {
		t.Fatalf("volume = %+v, want it sealed under %s", created, operator.ID())
	}

	stored, err := m.svc.repo.byID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.KeySealed == "" {
		t.Fatal("no sealed key was stored, so the node could never open the volume")
	}

	raw, err := sealed.OpenBytes(stored.KeySealed, operator)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if len(raw) != sealed.KeyBytes {
		t.Fatalf("key is %d bytes, want %d", len(raw), sealed.KeyBytes)
	}
}

func TestOnlyTheNodeHoldingAnEncryptedVolumeGetsItsKey(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))
	ctx := context.Background()

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"secret","size_gib":1,"encrypted":true}`)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, err := m.svc.keyForNode(ctx, created.ID, "n-nobody"); err == nil {
		t.Fatal("a node that does not hold the volume was handed its key")
	}

	if err := m.svc.repo.attach(ctx, created.ID, "i-1", "n-1", time.Now().UTC()); err != nil {
		t.Fatalf("attach: %v", err)
	}

	key, err := m.svc.keyForNode(ctx, created.ID, "n-1")
	if err != nil {
		t.Fatalf("the node holding it cannot get the key: %v", err)
	}
	if len(key) != sealed.KeyBytes*2 {
		t.Fatalf("key is %d characters, want %d hex", len(key), sealed.KeyBytes*2)
	}
}

func TestAPlainVolumeHasNoKeyToHandOut(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))
	ctx := context.Background()

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))

	rec := request(t, h, http.MethodPost, "/v1/volumes", `{"name":"plain","size_gib":1}`)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := m.svc.repo.attach(ctx, created.ID, "i-1", "n-1", time.Now().UTC()); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := m.svc.keyForNode(ctx, created.ID, "n-1"); err == nil {
		t.Fatal("a plaintext volume handed out a key")
	}
}

func TestAKeySealedWithAKeyNoLongerHeldIsNotGuessed(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))
	ctx := context.Background()

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"secret","size_gib":1,"encrypted":true}`)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := m.svc.repo.attach(ctx, created.ID, "i-1", "n-1", time.Now().UTC()); err != nil {
		t.Fatalf("attach: %v", err)
	}

	other, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{other}))

	_, err := m.svc.keyForNode(ctx, created.ID, "n-1")
	if err == nil {
		t.Fatal("the volume key was handed out despite its wrapping key being gone")
	}
	if !strings.Contains(err.Error(), operator.ID()) {
		t.Fatalf("err = %v, want it to name the missing key", err)
	}
}

type envelopeBackups struct {
	restorable Restorable
	err        error
}

func (b envelopeBackups) Restorable(context.Context, string, string) (Restorable, error) {
	return b.restorable, b.err
}

func TestAVolumeRestoredFromAnEncryptedBackupAdoptsItsKey(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))

	wrapped, err := sealed.SealBytes(make([]byte, sealed.KeyBytes), operator)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	m.UseBackups(envelopeBackups{restorable: Restorable{
		SizeBytes: 1024, KeySealed: wrapped, KeyID: operator.ID(),
	}})

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"restored","size_gib":1,"from_backup":"bkp-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !created.Encrypted {
		t.Fatal("the restored volume is not marked encrypted, so the node would write a " +
			"plain disk over an encrypted qcow2 and the guest would see nothing")
	}

	stored, err := m.svc.repo.byID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.KeySealed != wrapped {
		t.Fatal("the restored volume did not adopt the backup's key, so nothing could " +
			"open the bytes it is about to be filled with")
	}
}

func TestRestoringAPlaintextBackupIntoAnEncryptedVolumeIsRefused(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))
	m.UseBackups(envelopeBackups{restorable: Restorable{SizeBytes: 1024}})

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"restored","size_gib":1,"from_backup":"bkp-1","encrypted":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: the node writes a restore byte for byte, so this "+
			"would be a plaintext disk claiming to be encrypted", rec.Code, http.StatusConflict)
	}
}

func TestRestoringNeedsTheKeyThatWrappedTheBackupsVolumeKey(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))
	m.UseBackups(envelopeBackups{restorable: Restorable{
		SizeBytes: 1024, KeySealed: "whatever", KeyID: "a-key-nobody-holds",
	}})

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"restored","size_gib":1,"from_backup":"bkp-1"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "a-key-nobody-holds") {
		t.Fatalf("body = %q, want it to name the key", rec.Body.String())
	}
}

func TestAPlaintextRestoreStaysPlaintext(t *testing.T) {
	h, m := newTestModule(t, placedVM("n-1"))

	operator, _ := sealed.NewKey()
	m.UseKeys(sealed.NewKeyring([]sealed.Key{operator}))
	m.UseBackups(envelopeBackups{restorable: Restorable{SizeBytes: 1024}})

	rec := request(t, h, http.MethodPost, "/v1/volumes",
		`{"name":"restored","size_gib":1,"from_backup":"bkp-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Encrypted {
		t.Fatal("a plaintext backup produced a volume claiming encryption")
	}
}
