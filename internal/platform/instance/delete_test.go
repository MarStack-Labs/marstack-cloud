package instance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

type fakeNetworks struct {
	defaultID  string
	released   []string
	releaseErr error
}

func (f *fakeNetworks) DefaultNetworkID(context.Context) (string, error) {
	if f.defaultID == "" {
		return "nw-default", nil
	}
	return f.defaultID, nil
}

func (f *fakeNetworks) ReleaseAddress(_ context.Context, instanceID string) error {
	if f.releaseErr != nil {
		return f.releaseErr
	}
	f.released = append(f.released, instanceID)
	return nil
}

func newModuleWithNetworks(t *testing.T, networks Networks) (http.Handler, *Module) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard), networks)
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mux := http.NewServeMux()
	m.Routes(mux)
	return mux, m
}

func TestCreateUsesTheDefaultNetworkWhenNoneIsGiven(t *testing.T) {
	networks := &fakeNetworks{defaultID: "nw-abc"}
	h, _ := newModuleWithNetworks(t, networks)

	got := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances", validBody))
	if got.NetworkID != "nw-abc" {
		t.Fatalf("network_id = %q, want the resolved default", got.NetworkID)
	}
}

func TestDeleteReleasesTheAddress(t *testing.T) {
	networks := &fakeNetworks{}
	h, _ := newModuleWithNetworks(t, networks)

	created := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances", validBody))

	if rec := request(t, h, http.MethodDelete, "/v1/instances/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	if len(networks.released) != 1 || networks.released[0] != created.ID {
		t.Fatalf("released = %v, want [%s]: a deleted instance must not keep its address",
			networks.released, created.ID)
	}
}

func TestDeleteReportsAnUnreleasedAddress(t *testing.T) {
	networks := &fakeNetworks{releaseErr: errors.New("store unavailable")}
	h, _ := newModuleWithNetworks(t, networks)

	created := decodeInstance(t, request(t, h, http.MethodPost, "/v1/instances", validBody))

	rec := request(t, h, http.MethodDelete, "/v1/instances/"+created.ID, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: a leaked address must be visible", rec.Code, http.StatusInternalServerError)
	}
}

func TestDeleteOfAnUnknownInstanceDoesNotReleaseAnything(t *testing.T) {
	networks := &fakeNetworks{}
	h, _ := newModuleWithNetworks(t, networks)

	if rec := request(t, h, http.MethodDelete, "/v1/instances/i-missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if len(networks.released) != 0 {
		t.Fatalf("released = %v, want nothing", networks.released)
	}
}

func TestCreateWithoutACommandIsAllowed(t *testing.T) {
	h, _ := newModuleWithNetworks(t, &fakeNetworks{})

	rec := request(t, h, http.MethodPost, "/v1/instances",
		`{"name":"api-1","isolation":"container","image":"nginx:alpine"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: the image can supply the command", rec.Code, http.StatusCreated)
	}

	if got := decodeInstance(t, rec); len(got.Command) != 0 {
		t.Fatalf("command = %v, want it left empty so the node reads it from the image", got.Command)
	}
}

func TestCreateRejectsAnAbsurdlyLongCommand(t *testing.T) {
	h, _ := newModuleWithNetworks(t, &fakeNetworks{})

	args := make([]string, MaxCommandArgs+1)
	for i := range args {
		args[i] = `"x"`
	}
	body := `{"name":"api-1","isolation":"container","image":"alpine","command":[` +
		strings.Join(args, ",") + `]}`

	rec := request(t, h, http.MethodPost, "/v1/instances", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if code := errorCode(t, rec); code != "invalid_command" {
		t.Fatalf("error code = %q, want %q", code, "invalid_command")
	}
}
