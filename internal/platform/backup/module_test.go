package backup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

const (
	testProject   = "prj-test"
	testVolume    = "vol-1"
	testVolumeAka = "data"
	testNode      = "n-1"
)

type stubVolumes struct {
	nodeID string
}

func (s stubVolumes) Source(_ context.Context, volumeID, projectID string) (Source, error) {
	known := volumeID == testVolume || volumeID == testVolumeAka
	if !known || projectID != testProject {
		return Source{}, fault.NotFound("volume_not_found", "no volume with that id exists")
	}
	return Source{ID: testVolume, ProjectID: projectID, NodeID: s.nodeID, Name: testVolumeAka}, nil
}

func newTestModule(t *testing.T) (http.Handler, *Module, string) {
	return newModuleWithKeys(t, nil)
}

func newModuleWithKeys(t *testing.T, keys []sealed.Key) (http.Handler, *Module, string) {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()

	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m, err := New(st, dir, sealed.NewKeyring(keys), logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new module: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	m.UseVolumes(stubVolumes{nodeID: testNode})
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mux := http.NewServeMux()
	m.Routes(mux)
	return mux, m, dir
}

func request(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(scope.With(req.Context(), scope.Scope{ProjectID: testProject}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newBackup(t *testing.T, h http.Handler, name string) response {
	t.Helper()

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+testVolume+"/backups",
		`{"name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created
}

func upload(t *testing.T, h http.Handler, id, content string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPut,
		"/v1/nodes/"+testNode+"/backups/"+id+"/content", strings.NewReader(content))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestABackupStartsPendingAndNamesTheNodeThatHoldsTheVolume(t *testing.T) {
	h, _, _ := newTestModule(t)

	created := newBackup(t, h, "nightly")
	if created.State != StatePending {
		t.Fatalf("state = %q, want %q", created.State, StatePending)
	}
	if created.NodeID != testNode {
		t.Fatalf("node = %q, want the node holding the volume", created.NodeID)
	}
}

func TestAVolumeNoNodeHoldsCannotBeBackedUp(t *testing.T) {
	h, m, _ := newTestModule(t)
	m.UseVolumes(stubVolumes{nodeID: ""})

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+testVolume+"/backups", `{"name":"empty"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: there is nothing on disk to copy",
			rec.Code, http.StatusConflict)
	}
}

func TestUploadedContentLandsOutsideTheNodeAndIsChecksummed(t *testing.T) {
	h, _, dir := newTestModule(t)

	created := newBackup(t, h, "nightly")
	if rec := upload(t, h, created.ID, "volume-bytes"); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	rec := request(t, h, http.MethodGet, "/v1/backups/"+created.ID, "")
	var stored response
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stored.State != StateReady {
		t.Fatalf("state = %q, want %q", stored.State, StateReady)
	}
	if stored.SizeBytes != int64(len("volume-bytes")) {
		t.Fatalf("size = %d", stored.SizeBytes)
	}
	if stored.Checksum == "" {
		t.Fatal("no checksum was recorded, so a silent corruption would go unnoticed")
	}

	raw, err := os.ReadFile(filepath.Join(dir, DirName, created.ID))
	if err != nil {
		t.Fatalf("the bytes are not in the control plane's vault: %v", err)
	}
	if string(raw) != "volume-bytes" {
		t.Fatalf("stored %q", raw)
	}
}

func TestOnlyTheNodeHoldingTheVolumeMayUpload(t *testing.T) {
	h, _, _ := newTestModule(t)
	created := newBackup(t, h, "nightly")

	req := httptest.NewRequest(http.MethodPut,
		"/v1/nodes/n-somebody-else/backups/"+created.ID+"/content", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: another node must not write someone else's backup",
			rec.Code, http.StatusNotFound)
	}
}

func TestAPendingBackupServesNoContent(t *testing.T) {
	h, _, _ := newTestModule(t)
	created := newBackup(t, h, "nightly")

	req := httptest.NewRequest(http.MethodGet,
		"/v1/nodes/"+testNode+"/backups/"+created.ID+"/content", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestReadyContentIsServedBackWhole(t *testing.T) {
	h, _, _ := newTestModule(t)

	created := newBackup(t, h, "nightly")
	upload(t, h, created.ID, "volume-bytes")

	req := httptest.NewRequest(http.MethodGet,
		"/v1/nodes/"+testNode+"/backups/"+created.ID+"/content", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "volume-bytes" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestOnlyAPendingBackupIsHandedToItsNode(t *testing.T) {
	h, m, _ := newTestModule(t)

	first := newBackup(t, h, "one")
	newBackup(t, h, "two")
	upload(t, h, first.ID, "bytes")

	pending, err := m.svc.pendingOn(context.Background(), testNode)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Name != "two" {
		t.Fatalf("pending = %+v, want only the one still waiting", pending)
	}
}

func TestAFailureIsRecordedWithItsReason(t *testing.T) {
	h, _, _ := newTestModule(t)
	created := newBackup(t, h, "nightly")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/nodes/"+testNode+"/backups/"+created.ID+"/failure",
		strings.NewReader(`{"message":"qemu-img refused the write lock"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}

	rec = request(t, h, http.MethodGet, "/v1/backups/"+created.ID, "")
	var stored response
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stored.State != StateFailed || stored.Message == "" {
		t.Fatalf("stored = %+v, want a failure an operator can read", stored)
	}
}

func TestOnlyAReadyBackupCanBeRestoredFrom(t *testing.T) {
	h, m, _ := newTestModule(t)
	ctx := context.Background()

	created := newBackup(t, h, "nightly")
	if _, err := m.Restorable(ctx, created.ID, testProject); err == nil {
		t.Fatal("a pending backup was offered for restore, and it holds no bytes")
	}

	upload(t, h, created.ID, "volume-bytes")
	size, err := m.Restorable(ctx, created.ID, testProject)
	if err != nil {
		t.Fatalf("restorable: %v", err)
	}
	if size != int64(len("volume-bytes")) {
		t.Fatalf("size = %d", size)
	}
}

func TestABackupInAnotherProjectIsOutOfReach(t *testing.T) {
	h, m, _ := newTestModule(t)
	created := newBackup(t, h, "nightly")

	if _, err := m.Restorable(context.Background(), created.ID, "prj-other"); err == nil {
		t.Fatal("another project could restore from this backup")
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/backups/"+created.ID, nil)
	req = req.WithContext(scope.With(req.Context(), scope.Scope{ProjectID: "prj-other"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDeletingABackupTakesItsBytesWithIt(t *testing.T) {
	h, _, dir := newTestModule(t)

	created := newBackup(t, h, "nightly")
	upload(t, h, created.ID, "volume-bytes")

	if rec := request(t, h, http.MethodDelete, "/v1/backups/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}

	if _, err := os.Stat(filepath.Join(dir, DirName, created.ID)); !os.IsNotExist(err) {
		t.Fatal("the file outlived the record, so the disk fills up with backups nobody can see")
	}
}

func TestTwoBackupsOfOneVolumeCannotShareAName(t *testing.T) {
	h, _, _ := newTestModule(t)

	newBackup(t, h, "nightly")
	rec := request(t, h, http.MethodPost, "/v1/volumes/"+testVolume+"/backups", `{"name":"nightly"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func setSchedule(t *testing.T, h http.Handler, every string, keep int) scheduleResponse {
	t.Helper()

	body := `{"every":"` + every + `","keep":` + strconv.Itoa(keep) + `}`
	rec := request(t, h, http.MethodPut, "/v1/volumes/"+testVolume+"/schedule", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("set schedule: %d %s", rec.Code, rec.Body.String())
	}

	var sc scheduleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &sc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return sc
}

func TestAScheduleIsRefusedWhenItMakesNoSense(t *testing.T) {
	h, _, _ := newTestModule(t)

	for name, body := range map[string]string{
		"no interval":  `{"every":"","keep":3}`,
		"too often":    `{"every":"1s","keep":3}`,
		"nonsense":     `{"every":"whenever","keep":3}`,
		"keeps none":   `{"every":"1h","keep":0}`,
		"keeps absurd": `{"every":"1h","keep":9999}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := request(t, h, http.MethodPut,
				"/v1/volumes/"+testVolume+"/schedule", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestASweepTakesABackupWhenTheScheduleIsDue(t *testing.T) {
	h, m, _ := newTestModule(t)
	ctx := context.Background()

	now := time.Now().UTC()
	m.svc.now = func() time.Time { return now }

	setSchedule(t, h, "1h", 3)

	taken, _, err := m.svc.sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if taken != 0 {
		t.Fatalf("took %d backups before the interval passed", taken)
	}

	now = now.Add(time.Hour + time.Second)
	taken, _, err = m.svc.sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if taken != 1 {
		t.Fatalf("took %d, want exactly one", taken)
	}

	backups, err := m.svc.listIn(ctx, testProject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %d", len(backups))
	}
	if backups[0].ScheduleID == "" {
		t.Fatal("the backup does not name the schedule that made it, so retention " +
			"cannot tell it apart from one an operator took")
	}
}

func TestASweepDoesNotPileUpBehindAStuckNode(t *testing.T) {
	h, m, _ := newTestModule(t)
	ctx := context.Background()

	now := time.Now().UTC()
	m.svc.now = func() time.Time { return now }
	setSchedule(t, h, "1h", 5)

	for range 4 {
		now = now.Add(time.Hour + time.Second)
		if _, _, err := m.svc.sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
	}

	backups, err := m.svc.listIn(ctx, testProject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %d, want one: a node that never uploads must not collect a "+
			"queue of copies nobody can finish", len(backups))
	}
}

func TestRetentionDropsTheOldestCopiesItMade(t *testing.T) {
	h, m, dir := newTestModule(t)
	ctx := context.Background()

	now := time.Now().UTC()
	m.svc.now = func() time.Time { return now }
	setSchedule(t, h, "1h", 2)

	ids := make([]string, 0, 4)
	for range 4 {
		now = now.Add(time.Hour + time.Second)
		if _, _, err := m.svc.sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}

		backups, err := m.svc.listIn(ctx, testProject)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, b := range backups {
			if b.State != StatePending {
				continue
			}
			upload(t, h, b.ID, "bytes-"+b.Name)
			ids = append(ids, b.ID)
		}
	}

	backups, err := m.svc.listIn(ctx, testProject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(backups) != 2 {
		names := make([]string, 0, len(backups))
		for _, b := range backups {
			names = append(names, b.Name)
		}
		t.Fatalf("kept %v, want the newest two", names)
	}

	kept := map[string]bool{}
	for _, b := range backups {
		kept[b.ID] = true
	}
	for _, id := range ids {
		_, err := os.Stat(filepath.Join(dir, DirName, id))
		if kept[id] && err != nil {
			t.Fatalf("a kept backup lost its bytes: %v", err)
		}
		if !kept[id] && !os.IsNotExist(err) {
			t.Fatal("a pruned backup left its bytes behind, so the disk fills up with " +
				"copies no record points at")
		}
	}
}

func TestRetentionLeavesAManualBackupAlone(t *testing.T) {
	h, m, _ := newTestModule(t)
	ctx := context.Background()

	now := time.Now().UTC()
	m.svc.now = func() time.Time { return now }

	manual := newBackup(t, h, "before-upgrade")
	upload(t, h, manual.ID, "precious")

	setSchedule(t, h, "1h", 1)
	for range 3 {
		now = now.Add(time.Hour + time.Second)
		if _, _, err := m.svc.sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		backups, err := m.svc.listIn(ctx, testProject)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, b := range backups {
			if b.State == StatePending {
				upload(t, h, b.ID, "bytes")
			}
		}
	}

	if _, err := m.svc.getIn(ctx, manual.ID, testProject); err != nil {
		t.Fatalf("the manual backup was pruned by a schedule that did not make it: %v", err)
	}
}

func TestAScheduleIsReplacedRatherThanDuplicated(t *testing.T) {
	h, m, _ := newTestModule(t)

	first := setSchedule(t, h, "1h", 3)
	second := setSchedule(t, h, "6h", 7)

	if first.ID != second.ID {
		t.Fatalf("ids %s and %s: a volume carries one schedule", first.ID, second.ID)
	}
	if second.Keep != 7 || second.Every != "6h0m0s" {
		t.Fatalf("schedule = %+v, want the new terms", second)
	}

	schedules, err := m.svc.schedulesIn(context.Background(), testProject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedules = %d, want one", len(schedules))
	}
}

func TestAScheduleOnAnotherProjectsVolumeIsOutOfReach(t *testing.T) {
	h, _, _ := newTestModule(t)

	req := httptest.NewRequest(http.MethodPut, "/v1/volumes/"+testVolume+"/schedule",
		strings.NewReader(`{"every":"1h","keep":3}`))
	req = req.WithContext(scope.With(req.Context(), scope.Scope{ProjectID: "prj-other"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestABackupTakenByVolumeNameStillPointsAtTheVolumeID(t *testing.T) {
	h, _, _ := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/volumes/"+testVolumeAka+"/backups",
		`{"name":"by-name"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.VolumeID != testVolume {
		t.Fatalf("volume = %q, want %q: an agent matches its volumes by id, so a name "+
			"stored here is a backup no node will ever pick up", created.VolumeID, testVolume)
	}
}

func TestAScheduleSetByVolumeNameStillPointsAtTheVolumeID(t *testing.T) {
	h, m, _ := newTestModule(t)
	ctx := context.Background()

	rec := request(t, h, http.MethodPut, "/v1/volumes/"+testVolumeAka+"/schedule",
		`{"every":"1h","keep":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set schedule: %d %s", rec.Code, rec.Body.String())
	}

	var sc scheduleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &sc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sc.VolumeID != testVolume {
		t.Fatalf("volume = %q, want %q", sc.VolumeID, testVolume)
	}

	byID, err := m.svc.scheduleOf(ctx, testVolume, testProject)
	if err != nil {
		t.Fatalf("the schedule cannot be found by the volume id: %v", err)
	}
	byName, err := m.svc.scheduleOf(ctx, testVolumeAka, testProject)
	if err != nil {
		t.Fatalf("the schedule cannot be found by the volume name: %v", err)
	}
	if byID.ID != byName.ID {
		t.Fatal("the name and the id found different schedules")
	}
}

func TestRetentionFollowsTheScheduleThatMadeTheCopy(t *testing.T) {
	h, m, _ := newTestModule(t)
	ctx := context.Background()

	now := time.Now().UTC()
	m.svc.now = func() time.Time { return now }

	first := setSchedule(t, h, "1h", 1)

	for range 3 {
		now = now.Add(time.Hour + time.Second)
		if _, _, err := m.svc.sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}

		backups, err := m.svc.listIn(ctx, testProject)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, b := range backups {
			if b.State == StatePending {
				upload(t, h, b.ID, "bytes-"+b.Name)
			}
		}
	}

	replaced := setSchedule(t, h, "2h", 1)
	if replaced.ID != first.ID {
		t.Fatal("replacing a schedule should keep its id")
	}

	backups, err := m.svc.listIn(ctx, testProject)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(backups) != 1 {
		names := make([]string, 0, len(backups))
		for _, b := range backups {
			names = append(names, b.Name)
		}
		t.Fatalf("kept %v, want one: retention must follow the schedule that made a copy, "+
			"not whichever schedule the volume happens to carry now", names)
	}
}

func fetchContent(t *testing.T, h http.Handler, id string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet,
		"/v1/nodes/"+testNode+"/backups/"+id+"/content", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestASealedBackupIsUnreadableOnDiskAndWholeOverTheWire(t *testing.T) {
	key, _ := sealed.NewKey()
	h, _, dir := newModuleWithKeys(t, []sealed.Key{key})

	secret := strings.Repeat("volume bytes nobody else should read ", 4096)
	created := newBackup(t, h, "nightly")
	if rec := upload(t, h, created.ID, secret); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	onDisk, err := os.ReadFile(filepath.Join(dir, DirName, created.ID))
	if err != nil {
		t.Fatalf("read the vault: %v", err)
	}
	if strings.Contains(string(onDisk), "volume bytes nobody") {
		t.Fatal("the volume is readable in the data directory, so anybody with the disk " +
			"has the customer's data")
	}

	rec := request(t, h, http.MethodGet, "/v1/backups/"+created.ID, "")
	var stored response
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stored.KeyID != key.ID() {
		t.Fatalf("key = %q, want %q recorded so rotation does not orphan it",
			stored.KeyID, key.ID())
	}
	if stored.SizeBytes != int64(len(secret)) {
		t.Fatalf("size = %d, want the plaintext length %d", stored.SizeBytes, len(secret))
	}

	back := fetchContent(t, h, created.ID)
	if back.Code != http.StatusOK {
		t.Fatalf("download: %d %s", back.Code, back.Body.String())
	}
	if back.Body.String() != secret {
		t.Fatalf("read back %d bytes, want %d", back.Body.Len(), len(secret))
	}
	if got := back.Header().Get("Content-Length"); got != strconv.Itoa(len(secret)) {
		t.Fatalf("Content-Length = %q, want the plaintext length: a node sizing the disk "+
			"from the ciphertext writes the wrong thing", got)
	}
}

func TestTheChecksumCoversThePlaintextNotTheCiphertext(t *testing.T) {
	key, _ := sealed.NewKey()
	sealedHandler, _, _ := newModuleWithKeys(t, []sealed.Key{key})
	plainHandler, _, _ := newTestModule(t)

	content := strings.Repeat("same bytes ", 1000)

	sums := make([]string, 0, 2)
	for _, h := range []http.Handler{sealedHandler, plainHandler} {
		created := newBackup(t, h, "nightly")
		upload(t, h, created.ID, content)

		rec := request(t, h, http.MethodGet, "/v1/backups/"+created.ID, "")
		var stored response
		if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
			t.Fatalf("decode: %v", err)
		}
		sums = append(sums, stored.Checksum)
	}

	if sums[0] != sums[1] {
		t.Fatalf("checksums %s and %s differ, so an operator cannot compare a sealed backup "+
			"with the disk it came from", sums[0], sums[1])
	}
}

func TestABackupTakenBeforeEncryptionStaysReadable(t *testing.T) {
	h, m, _ := newTestModule(t)

	created := newBackup(t, h, "from-the-past")
	upload(t, h, created.ID, "plain bytes")

	key, _ := sealed.NewKey()
	m.svc.vault.keys = sealed.NewKeyring([]sealed.Key{key})

	back := fetchContent(t, h, created.ID)
	if back.Code != http.StatusOK {
		t.Fatalf("download: %d %s: turning encryption on must not orphan what came before",
			back.Code, back.Body.String())
	}
	if back.Body.String() != "plain bytes" {
		t.Fatalf("body = %q", back.Body.String())
	}
}

func TestABackupSealedWithARetiredKeyStaysReadable(t *testing.T) {
	old, _ := sealed.NewKey()
	h, m, _ := newModuleWithKeys(t, []sealed.Key{old})

	created := newBackup(t, h, "sealed-with-old")
	upload(t, h, created.ID, "old bytes")

	fresh, _ := sealed.NewKey()
	m.svc.vault.keys = sealed.NewKeyring([]sealed.Key{fresh, old})

	back := fetchContent(t, h, created.ID)
	if back.Code != http.StatusOK {
		t.Fatalf("download: %d %s", back.Code, back.Body.String())
	}
	if back.Body.String() != "old bytes" {
		t.Fatalf("body = %q", back.Body.String())
	}

	next := newBackup(t, h, "sealed-with-fresh")
	upload(t, h, next.ID, "new bytes")

	rec := request(t, h, http.MethodGet, "/v1/backups/"+next.ID, "")
	var stored response
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stored.KeyID != fresh.ID() {
		t.Fatalf("key = %q, want the new backup sealed with the active key %q",
			stored.KeyID, fresh.ID())
	}
}

func TestABackupWhoseKeyIsGoneSaysSoInsteadOfServingRubbish(t *testing.T) {
	key, _ := sealed.NewKey()
	h, m, _ := newModuleWithKeys(t, []sealed.Key{key})

	created := newBackup(t, h, "orphaned")
	upload(t, h, created.ID, "bytes")

	m.svc.vault.keys = sealed.NewKeyring(nil)

	back := fetchContent(t, h, created.ID)
	if back.Code == http.StatusOK {
		t.Fatal("the ciphertext was served as though it were the volume")
	}
	if !strings.Contains(back.Body.String(), key.ID()) {
		t.Fatalf("body = %q, want it to name the key that is missing", back.Body.String())
	}
}

func TestAnEncryptedVolumeIsRefusedUpFrontNotOncePerCycle(t *testing.T) {
	h, m, _ := newTestModule(t)
	m.UseVolumes(encryptedVolume{})

	for name, call := range map[string]struct{ method, path, body string }{
		"backup": {http.MethodPost, "/v1/volumes/" + testVolume + "/backups", `{"name":"x"}`},
		"schedule": {http.MethodPut, "/v1/volumes/" + testVolume + "/schedule",
			`{"every":"1d","keep":3}`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := request(t, h, call.method, call.path, call.body)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d: a schedule that can only ever fail is worse "+
					"than no schedule", rec.Code, http.StatusConflict)
			}
		})
	}
}

type encryptedVolume struct{}

func (encryptedVolume) Source(_ context.Context, volumeID, projectID string) (Source, error) {
	if volumeID != testVolume || projectID != testProject {
		return Source{}, fault.NotFound("volume_not_found", "no volume with that id exists")
	}
	return Source{
		ID:        testVolume,
		ProjectID: projectID,
		NodeID:    testNode,
		Name:      testVolumeAka,
		Encrypted: true,
	}, nil
}
