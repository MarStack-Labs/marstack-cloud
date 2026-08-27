package project

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type fixedOccupancy int

func (f fixedOccupancy) ResourcesIn(context.Context, string) (int, error) {
	return int(f), nil
}

func newTestModule(t *testing.T) (http.Handler, *Module) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard))
	m.UseOccupancy(fixedOccupancy(0))
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

func TestTheDefaultProjectIsCreatedOnceWithAStableID(t *testing.T) {
	_, m := newTestModule(t)
	ctx := context.Background()

	first, err := m.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if first.ID != DefaultID {
		t.Fatalf("id = %q, want %q: every migration backfills that literal", first.ID, DefaultID)
	}

	second, err := m.EnsureDefault(ctx)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("a second start made another project, so existing resources would be orphaned")
	}
}

func TestTheDefaultProjectCannotBeDeleted(t *testing.T) {
	h, m := newTestModule(t)
	if _, err := m.EnsureDefault(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	rec := request(t, h, http.MethodDelete, "/v1/projects/"+DefaultID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestAProjectHoldingResourcesCannotBeDeleted(t *testing.T) {
	h, m := newTestModule(t)
	m.UseOccupancy(fixedOccupancy(1))

	rec := request(t, h, http.MethodPost, "/v1/projects", `{"name":"busy"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rec = request(t, h, http.MethodDelete, "/v1/projects/"+created.ID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: deleting it would strand what it holds",
			rec.Code, http.StatusConflict)
	}
}

func TestAnEmptyProjectIsDeleted(t *testing.T) {
	h, _ := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/projects", `{"name":"spare"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if rec := request(t, h, http.MethodDelete, "/v1/projects/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestDeleteRefusesWhenEmptinessCannotBeProven(t *testing.T) {
	h, m := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/projects", `{"name":"unknown"}`)
	var created response
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	m.svc.occupancy = nil
	rec = request(t, h, http.MethodDelete, "/v1/projects/"+created.ID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: an unwired counter must fail closed, not delete",
			rec.Code, http.StatusInternalServerError)
	}
}

func TestTwoProjectsCannotShareAName(t *testing.T) {
	h, _ := newTestModule(t)

	if rec := request(t, h, http.MethodPost, "/v1/projects", `{"name":"twin"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := request(t, h, http.MethodPost, "/v1/projects", `{"name":"twin"}`); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}
