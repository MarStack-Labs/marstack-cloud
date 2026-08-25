package instance

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

func newTestModule(t *testing.T) http.Handler {
	t.Helper()

	h, _ := newTestModuleWithAssign(t)
	return h
}

func newTestModuleWithAssign(t *testing.T) (http.Handler, *Module) {
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

func decodeInstance(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()

	var got response
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode instance: %v (body: %s)", err, rec.Body.String())
	}
	return got
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error: %v (body: %s)", err, rec.Body.String())
	}
	return body.Error.Code
}

const validBody = `{"name":"api-1","isolation":"container","image":"alpine:3.20"}`

func TestCreateAppliesDefaultsAndStartsPending(t *testing.T) {
	h := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/instances", validBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}

	got := decodeInstance(t, rec)
	if !strings.HasPrefix(got.ID, "i-") {
		t.Errorf("id = %q, want an i- prefixed id", got.ID)
	}
	if got.VCPU != DefaultVCPU {
		t.Errorf("vcpu = %d, want %d", got.VCPU, DefaultVCPU)
	}
	if got.MemoryMiB != DefaultMemoryMiB {
		t.Errorf("memory_mib = %d, want %d", got.MemoryMiB, DefaultMemoryMiB)
	}
	if got.Desired != string(DesiredRunning) {
		t.Errorf("desired_state = %q, want %q", got.Desired, DesiredRunning)
	}
	if got.Observed != string(ObservedPending) {
		t.Errorf("observed_state = %q, want %q", got.Observed, ObservedPending)
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	h := newTestModule(t)

	request(t, h, http.MethodPost, "/v1/instances", validBody)
	rec := request(t, h, http.MethodPost, "/v1/instances", validBody)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if code := errorCode(t, rec); code != "instance_name_taken" {
		t.Fatalf("error code = %q, want %q", code, "instance_name_taken")
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"empty name", `{"name":"","isolation":"container","image":"alpine"}`, "invalid_name"},
		{"uppercase name", `{"name":"API-1","isolation":"container","image":"alpine"}`, "invalid_name"},
		{"unknown isolation", `{"name":"api-1","isolation":"jail","image":"alpine"}`, "invalid_isolation"},
		{"empty image", `{"name":"api-1","isolation":"container","image":""}`, "invalid_image"},
		{"vcpu too high", `{"name":"api-1","isolation":"container","image":"alpine","vcpu":9999}`, "invalid_vcpu"},
		{"memory too low", `{"name":"api-1","isolation":"container","image":"alpine","memory_mib":1}`, "invalid_memory"},
		{"unknown field", `{"name":"api-1","isolation":"container","image":"alpine","root":true}`, "invalid_json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(t, newTestModule(t), http.MethodPost, "/v1/instances", tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if code := errorCode(t, rec); code != tc.wantCode {
				t.Fatalf("error code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestListReturnsEmptyArrayNotNull(t *testing.T) {
	rec := request(t, newTestModule(t), http.MethodGet, "/v1/instances", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, `"instances": []`) {
		t.Fatalf("body = %s, want an empty instances array", body)
	}
}

func TestGetReturnsCreatedInstance(t *testing.T) {
	h := newTestModule(t)
	created := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances", validBody))

	rec := request(t, h, http.MethodGet, "/v1/instances/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := decodeInstance(t, rec); got.ID != created.ID {
		t.Fatalf("id = %q, want %q", got.ID, created.ID)
	}
}

func TestGetUnknownInstanceIsNotFound(t *testing.T) {
	rec := request(t, newTestModule(t), http.MethodGet, "/v1/instances/i-missing", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if code := errorCode(t, rec); code != "instance_not_found" {
		t.Fatalf("error code = %q, want %q", code, "instance_not_found")
	}
}

func TestStopThenStartChangesDesiredStateOnly(t *testing.T) {
	h := newTestModule(t)
	created := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances", validBody))

	stopped := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances/"+created.ID+"/stop", ""))
	if stopped.Desired != string(DesiredStopped) {
		t.Fatalf("desired_state = %q, want %q", stopped.Desired, DesiredStopped)
	}
	if stopped.Observed != created.Observed {
		t.Fatalf("observed_state = %q, want it unchanged at %q", stopped.Observed, created.Observed)
	}

	started := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances/"+created.ID+"/start", ""))
	if started.Desired != string(DesiredRunning) {
		t.Fatalf("desired_state = %q, want %q", started.Desired, DesiredRunning)
	}
}

func TestStopUnknownInstanceIsNotFound(t *testing.T) {
	rec := request(t, newTestModule(t), http.MethodPost, "/v1/instances/i-missing/stop", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDeleteRemovesInstance(t *testing.T) {
	h := newTestModule(t)
	created := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances", validBody))

	if rec := request(t, h, http.MethodDelete, "/v1/instances/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec := request(t, h, http.MethodGet, "/v1/instances/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status after delete = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDeleteFreesTheName(t *testing.T) {
	h := newTestModule(t)
	created := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances", validBody))
	request(t, h, http.MethodDelete, "/v1/instances/"+created.ID, "")

	if rec := request(t, h, http.MethodPost, "/v1/instances", validBody); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
}
