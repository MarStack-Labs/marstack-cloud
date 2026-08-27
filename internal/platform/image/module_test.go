package image

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

const ubuntu = `{"name":"ubuntu-24.04","kind":"disk",` +
	`"source":"https://cloud-images.ubuntu.com/releases/24.04/release/x.img"}`

func newTestModule(t *testing.T) (http.Handler, *Module) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard))
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mux := http.NewServeMux()
	m.Routes(mux)
	return mux, m
}

const testProject = "prj-test"

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
	req = req.WithContext(scope.With(req.Context(), scope.Scope{ProjectID: testProject}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegisteringAnImage(t *testing.T) {
	h, _ := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/images", ubuntu)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Kind != KindDisk {
		t.Fatalf("kind = %q", created.Kind)
	}
	if created.Arch != ArchARM64 {
		t.Fatalf("arch = %q, want the node architecture default", created.Arch)
	}
	if !strings.HasPrefix(created.ID, "img-") {
		t.Fatalf("id = %q", created.ID)
	}
}

func TestImagesAreFoundByNameOrID(t *testing.T) {
	h, m := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/images", ubuntu)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, key := range []string{created.ID, created.Name} {
		found, err := m.svc.resolveIn(context.Background(), key, testProject)
		if err != nil {
			t.Fatalf("resolve %q: %v", key, err)
		}
		if found.ID != created.ID {
			t.Fatalf("resolve %q gave %q", key, found.ID)
		}
	}

	if rec := request(t, h, http.MethodGet, "/v1/images/ubuntu-24.04", ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want a name to work on the path too", rec.Code)
	}
	if rec := request(t, h, http.MethodGet, "/v1/images/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestBadImagesAreRejected(t *testing.T) {
	h, _ := newTestModule(t)

	cases := map[string]struct {
		body string
		want int
	}{
		"no name": {
			`{"kind":"disk","source":"https://example.test/a.img"}`, http.StatusBadRequest},
		"unknown kind": {
			`{"name":"a","kind":"floppy","source":"https://example.test/a.img"}`, http.StatusBadRequest},
		"unknown arch": {
			`{"name":"a","kind":"iso","arch":"riscv","source":"https://example.test/a.iso"}`,
			http.StatusBadRequest},
		"source is not a url": {
			`{"name":"a","kind":"disk","source":"/var/lib/marstack/images/a.qcow2"}`,
			http.StatusBadRequest},
		"source has no host": {
			`{"name":"a","kind":"disk","source":"https:///a.img"}`, http.StatusBadRequest},
		"checksum is not sha256": {
			`{"name":"a","kind":"disk","source":"https://example.test/a.img","checksum":"deadbeef"}`,
			http.StatusBadRequest},
		"traversal in the name": {
			`{"name":"a..b","kind":"disk","source":"https://example.test/a.img"}`,
			http.StatusBadRequest},
		"slash in the name": {
			`{"name":"a/b","kind":"disk","source":"https://example.test/a.img"}`,
			http.StatusBadRequest},
		"unknown field": {
			`{"name":"a","kind":"disk","source":"https://example.test/a.img","url":"x"}`,
			http.StatusBadRequest},
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := request(t, h, http.MethodPost, "/v1/images", want.body); rec.Code != want.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, want.want, rec.Body.String())
			}
		})
	}
}

func TestDuplicateImageNameIsRejected(t *testing.T) {
	h, _ := newTestModule(t)

	request(t, h, http.MethodPost, "/v1/images", ubuntu)
	if rec := request(t, h, http.MethodPost, "/v1/images", ubuntu); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestDeletingAnImage(t *testing.T) {
	h, _ := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/images", ubuntu)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if rec := request(t, h, http.MethodDelete, "/v1/images/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec := request(t, h, http.MethodDelete, "/v1/images/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d on a second delete", rec.Code, http.StatusNotFound)
	}
}

func TestAChecksumIsKeptLowercase(t *testing.T) {
	h, _ := newTestModule(t)

	digest := strings.Repeat("AB", 32)
	body := `{"name":"a","kind":"iso","source":"https://example.test/a.iso",` +
		`"checksum":"sha256:` + digest + `"}`

	rec := request(t, h, http.MethodPost, "/v1/images", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Checksum != "sha256:"+strings.ToLower(digest) {
		t.Fatalf("checksum = %q, want it normalised so comparing digests never depends on case",
			created.Checksum)
	}
}

func TestNodesReportWhatTheyHaveStaged(t *testing.T) {
	h, _ := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/images", ubuntu)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	body := `{"images":[{"image_id":"` + created.ID + `","size_bytes":618659840}]}`
	if rec := request(t, h, http.MethodPut, "/v1/nodes/n-1/images", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := request(t, h, http.MethodPut, "/v1/nodes/n-2/images", body); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}

	var list listResponse
	if err := json.Unmarshal(request(t, h, http.MethodGet, "/v1/images", "").Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Images) != 1 {
		t.Fatalf("images = %d", len(list.Images))
	}
	if len(list.Images[0].Nodes) != 2 {
		t.Fatalf("nodes = %v, want both nodes that reported it", list.Images[0].Nodes)
	}

	if rec := request(t, h, http.MethodPut, "/v1/nodes/n-1/images", `{"images":[]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}

	if err := json.Unmarshal(request(t, h, http.MethodGet, "/v1/images", "").Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Images[0].Nodes) != 1 || list.Images[0].Nodes[0] != "n-2" {
		t.Fatalf("nodes = %v, want a node that dropped the image to disappear from the list",
			list.Images[0].Nodes)
	}
}

func TestAnImageSizeComesFromTheNodesThatHaveIt(t *testing.T) {
	h, _ := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/images", ubuntu)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.SizeBytes != 0 {
		t.Fatalf("size = %d, want it unknown until a node has downloaded it", created.SizeBytes)
	}

	body := `{"images":[{"image_id":"` + created.ID + `","size_bytes":4096}]}`
	request(t, h, http.MethodPut, "/v1/nodes/n-1/images", body)

	var one response
	if err := json.Unmarshal(
		request(t, h, http.MethodGet, "/v1/images/"+created.Name, "").Body.Bytes(), &one,
	); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if one.SizeBytes != 4096 {
		t.Fatalf("size = %d, want the size a node measured", one.SizeBytes)
	}
}
