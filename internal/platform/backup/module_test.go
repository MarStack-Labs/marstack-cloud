package backup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

const (
	testProject = "prj-test"
	testVolume  = "vol-1"
	testNode    = "n-1"
)

type stubVolumes struct {
	nodeID string
}

func (s stubVolumes) Source(_ context.Context, volumeID, projectID string) (Source, error) {
	if volumeID != testVolume || projectID != testProject {
		return Source{}, fault.NotFound("volume_not_found", "no volume with that id exists")
	}
	return Source{ProjectID: projectID, NodeID: s.nodeID, Name: "data"}, nil
}

func newTestModule(t *testing.T) (http.Handler, *Module, string) {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()

	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m, err := New(st, dir, logging.New("error", io.Discard))
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
