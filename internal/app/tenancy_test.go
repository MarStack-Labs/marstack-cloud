package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/platform/project"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

func newProject(t *testing.T, a *testApp, name string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/projects",
		strings.NewReader(`{"name":"`+name+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func tokenIn(t *testing.T, a *testApp, name, projectID string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"`+name+`","role":"member","project_id":"`+projectID+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.Secret
}

func newInstance(t *testing.T, a *testApp, secret, name string) string {
	t.Helper()

	body := `{"name":"` + name + `","isolation":"container","image":"alpine:3.20"}`
	rec := doAs(t, a, secret, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create instance: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func instanceNames(t *testing.T, a *testApp, secret string) []string {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodGet, "/v1/instances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Instances []struct {
			Name string `json:"name"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	names := make([]string, 0, len(body.Instances))
	for _, in := range body.Instances {
		names = append(names, in.Name)
	}
	return names
}

func TestAProjectSeesOnlyItsOwnInstances(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	newInstance(t, a, a.secret, "ours")
	newInstance(t, a, theirs, "theirs")

	mine := instanceNames(t, a, a.secret)
	if len(mine) != 1 || mine[0] != "ours" {
		t.Fatalf("the default project sees %v, want only its own", mine)
	}

	yours := instanceNames(t, a, theirs)
	if len(yours) != 1 || yours[0] != "theirs" {
		t.Fatalf("tenant-b sees %v, want only its own", yours)
	}
}

func TestAnInstanceInAnotherProjectIsNotFoundRatherThanForbidden(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)
	id := newInstance(t, a, a.secret, "private")

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/instances/" + id},
		{http.MethodDelete, "/v1/instances/" + id},
		{http.MethodPost, "/v1/instances/" + id + "/start"},
		{http.MethodPost, "/v1/instances/" + id + "/stop"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := doAs(t, a, theirs, c.method, c.path, nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d: a 403 would confirm the id exists",
					rec.Code, http.StatusNotFound)
			}
		})
	}
}

func TestTwoProjectsMayEachHaveAnInstanceOfTheSameName(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	newInstance(t, a, a.secret, "web")
	newInstance(t, a, theirs, "web")
}

func TestEachProjectGetsItsOwnNetworkOnADistinctRange(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	newInstance(t, a, a.secret, "ours")
	newInstance(t, a, theirs, "theirs")

	ranges := map[string]string{}
	for name, secret := range map[string]string{"default": a.secret, "tenant-b": theirs} {
		rec := doAs(t, a, secret, http.MethodGet, "/v1/networks", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s list networks: %d %s", name, rec.Code, rec.Body.String())
		}

		var body struct {
			Networks []struct {
				CIDR string `json:"cidr"`
			} `json:"networks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Networks) != 1 {
			t.Fatalf("%s sees %d networks, want exactly its own", name, len(body.Networks))
		}
		ranges[name] = body.Networks[0].CIDR
	}

	if ranges["default"] == ranges["tenant-b"] {
		t.Fatalf("both projects landed on %s, and routing carries no encapsulation, so two "+
			"projects on one range would collide on the wire", ranges["default"])
	}
}

func TestAProjectHoldingAnInstanceCannotBeDeleted(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)
	newInstance(t, a, theirs, "busy")

	rec := do(t, a, http.MethodDelete, "/v1/projects/"+other, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestTheDefaultProjectExistsAtBoot(t *testing.T) {
	a := newTestApp(t)

	rec := do(t, a, http.MethodGet, "/v1/projects/"+project.DefaultID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: every migration backfills that id", rec.Code, http.StatusOK)
	}
}

func createIn(t *testing.T, a *testApp, secret, path, body string) string {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodPost, path, strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s: %d %s", path, rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ID
}

func countAt(t *testing.T, a *testApp, secret, path, field string) int {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var rows []json.RawMessage
	if err := json.Unmarshal(body[field], &rows); err != nil {
		t.Fatalf("decode %s: %v", field, err)
	}
	return len(rows)
}

func TestVolumesAndImagesAreInvisibleAcrossProjects(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	createIn(t, a, a.secret, "/v1/volumes", `{"name":"data","size_gib":1}`)
	createIn(t, a, a.secret, "/v1/images",
		`{"name":"golden","kind":"disk","source":"https://example.invalid/g.qcow2"}`)

	for _, c := range []struct{ path, field string }{
		{"/v1/volumes", "volumes"},
		{"/v1/images", "images"},
	} {
		t.Run(c.path, func(t *testing.T) {
			if got := countAt(t, a, theirs, c.path, c.field); got != 0 {
				t.Fatalf("tenant-b sees %d rows at %s, want none", got, c.path)
			}
			if got := countAt(t, a, a.secret, c.path, c.field); got != 1 {
				t.Fatalf("the owner sees %d rows at %s, want its own", got, c.path)
			}
		})
	}
}

func TestAVolumeCannotBeAttachedToAnInstanceInAnotherProject(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	volumeID := createIn(t, a, theirs, "/v1/volumes", `{"name":"theirs","size_gib":1}`)
	instanceID := newInstance(t, a, a.secret, "ours")

	rec := doAs(t, a, theirs, http.MethodPost, "/v1/volumes/"+volumeID+"/attach",
		strings.NewReader(`{"instance_id":"`+instanceID+`"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: a disk must not cross a tenant boundary",
			rec.Code, http.StatusNotFound)
	}
}

func TestAnInstanceCannotBorrowAnotherProjectsFirewall(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	firewallID := createIn(t, a, a.secret, "/v1/firewalls", `{"name":"ours","rules":[]}`)

	body := `{"name":"borrowed","isolation":"container","image":"alpine:3.20",` +
		`"firewall_id":"` + firewallID + `"}`
	rec := doAs(t, a, theirs, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: borrowing a firewall would let one tenant open "+
			"another tenant's ports by editing rules", rec.Code, http.StatusBadRequest)
	}
}

func TestPublishingAnotherProjectsInstanceIsRefused(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)
	instanceID := newInstance(t, a, a.secret, "ours")

	rec := doAs(t, a, theirs, http.MethodPost, "/v1/forwards",
		strings.NewReader(`{"instance_id":"`+instanceID+`","target_port":80}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: publishing would expose another tenant's workload",
			rec.Code, http.StatusNotFound)
	}
}

func TestSnapshotsOfAnotherProjectsVolumeAreOutOfReach(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	volumeID := createIn(t, a, a.secret, "/v1/volumes", `{"name":"data","size_gib":1}`)

	rec := doAs(t, a, theirs, http.MethodGet, "/v1/volumes/"+volumeID+"/snapshots", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: the snapshot list names what is on that disk",
			rec.Code, http.StatusNotFound)
	}

	rec = doAs(t, a, theirs, http.MethodPost, "/v1/volumes/"+volumeID+"/snapshots",
		strings.NewReader(`{"name":"stolen"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	if got := countAt(t, a, theirs, "/v1/snapshots", "snapshots"); got != 0 {
		t.Fatalf("tenant-b sees %d snapshots, want none", got)
	}
}

func TestAVolumeCannotBeRestoredFromABackupThatDoesNotExist(t *testing.T) {
	a := newTestApp(t)

	rec := do(t, a, http.MethodPost, "/v1/volumes",
		strings.NewReader(`{"name":"restored","size_gib":1,"from_backup":"bkp-nope"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: a typo here would silently create an empty disk "+
			"where the operator expected their data", rec.Code, http.StatusNotFound)
	}
}

func TestBackupsAreScopedToTheirProject(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	if got := countAt(t, a, theirs, "/v1/backups", "backups"); got != 0 {
		t.Fatalf("tenant-b sees %d backups, want none", got)
	}

	volumeID := createIn(t, a, a.secret, "/v1/volumes", `{"name":"data","size_gib":1}`)
	rec := doAs(t, a, theirs, http.MethodGet, "/v1/volumes/"+volumeID+"/backups", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: the backup list names what was on that disk",
			rec.Code, http.StatusNotFound)
	}
}

func TestABackupNeedsANodeHoldingTheVolume(t *testing.T) {
	a := newTestApp(t)

	volumeID := createIn(t, a, a.secret, "/v1/volumes", `{"name":"data","size_gib":1}`)
	rec := do(t, a, http.MethodPost, "/v1/volumes/"+volumeID+"/backups",
		strings.NewReader(`{"name":"nightly"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: an unattached volume has no bytes anywhere",
			rec.Code, http.StatusConflict)
	}
}

func TestAMemberMayBackUpButNotReachTheNodeTransfer(t *testing.T) {
	a := newTestApp(t)
	secret := memberToken(t, a)

	if rec := doAs(t, a, secret, http.MethodGet, "/v1/backups", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want a member to see its own backups", rec.Code)
	}

	rec := doAs(t, a, secret, http.MethodGet, "/v1/nodes/n-1/backups", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: the node queue is not an operator endpoint",
			rec.Code, http.StatusForbidden)
	}
}

func newSlowApp(t *testing.T, timeout time.Duration) *testApp {
	t.Helper()

	dir := t.TempDir()
	a, err := New(context.Background(),
		Config{DataDir: dir, RequestTimeout: timeout, RatePerSecond: &unlimited}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}
	return &testApp{App: a, secret: strings.TrimSpace(string(raw))}
}

func TestMovingABackupIsNotBoundByTheRequestTimeout(t *testing.T) {
	a := newSlowApp(t, time.Nanosecond)

	rec := do(t, a, http.MethodGet, "/v1/instances", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: an ordinary request is still bounded",
			rec.Code, http.StatusServiceUnavailable)
	}

	rec = do(t, a, http.MethodGet, "/v1/nodes/n-1/backups/bkp-nope/content", nil)
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("moving a volume was cut off by the request timeout, so any backup larger " +
			"than a few seconds of transfer could never finish")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the handler to have actually run", rec.Code)
	}
}

func TestTheTrailOfOneProjectDoesNotShowAnother(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirsAdmin := adminTokenIn(t, a, "b-admin", other)

	newInstance(t, a, a.secret, "ours")

	for _, entry := range trailAs(t, a, theirsAdmin) {
		if entry.Method == http.MethodPost && entry.Path == "/v1/instances" {
			t.Fatalf("tenant-b can read that the default project created an instance: %+v", entry)
		}
	}

	found := false
	for _, entry := range trailAs(t, a, a.secret) {
		if entry.Method == http.MethodPost && entry.Path == "/v1/instances" {
			found = true
			if entry.ProjectID == "" {
				t.Fatal("the entry names no project, so the trail cannot be read per tenant")
			}
		}
	}
	if !found {
		t.Fatal("the owner cannot see its own create")
	}
}

func TestUnauthenticatedRefusalsStayVisibleToEveryAdmin(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirsAdmin := adminTokenIn(t, a, "b-admin", other)

	doAs(t, a, "", http.MethodPost, "/v1/instances", strings.NewReader(`{}`))

	for _, entry := range trailAs(t, a, theirsAdmin) {
		if entry.Status == http.StatusUnauthorized {
			return
		}
	}
	t.Fatal("a refused request with no proven identity belongs to no project, so hiding it " +
		"from every project hides it from everyone")
}

func adminTokenIn(t *testing.T, a *testApp, name, projectID string) string {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/tokens",
		strings.NewReader(`{"name":"`+name+`","role":"admin","project_id":"`+projectID+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.Secret
}

func trailAs(t *testing.T, a *testApp, secret string) []struct {
	Actor     string `json:"actor"`
	ProjectID string `json:"project_id"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
} {
	t.Helper()

	var body struct {
		Entries []struct {
			Actor     string `json:"actor"`
			ProjectID string `json:"project_id"`
			Method    string `json:"method"`
			Path      string `json:"path"`
			Status    int    `json:"status"`
		} `json:"entries"`
	}
	rec := doAs(t, a, secret, http.MethodGet, "/v1/audit", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read the trail: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Entries
}
