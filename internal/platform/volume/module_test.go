package volume

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
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
