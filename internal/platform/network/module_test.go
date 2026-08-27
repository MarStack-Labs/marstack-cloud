package network

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

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var got T
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	return got
}

func TestEnsureDefaultIsIdempotent(t *testing.T) {
	_, m := newTestModule(t)
	ctx := context.Background()

	first, err := m.EnsureDefault(ctx, testProject)
	if err != nil {
		t.Fatalf("ensure default: %v", err)
	}
	second, err := m.EnsureDefault(ctx, testProject)
	if err != nil {
		t.Fatalf("ensure default again: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("second call created %q, want the original %q", second.ID, first.ID)
	}
	if first.CIDR != DefaultCIDR {
		t.Fatalf("cidr = %q, want %q", first.CIDR, DefaultCIDR)
	}
	if first.Gateway != "10.20.0.1" {
		t.Fatalf("gateway = %q, want the first usable address", first.Gateway)
	}
	if !strings.HasPrefix(first.Bridge, bridgePrefix) {
		t.Fatalf("bridge = %q, want a %s prefix", first.Bridge, bridgePrefix)
	}
	if len(first.Bridge) > 15 {
		t.Fatalf("bridge name %q is %d characters; Linux allows 15", first.Bridge, len(first.Bridge))
	}
}

func TestCreateRejectsBadCIDR(t *testing.T) {
	h, _ := newTestModule(t)

	cases := map[string]string{
		"not a cidr": `{"name":"bad","cidr":"10.20.0.0"}`,
		"ipv6":       `{"name":"bad","cidr":"fd00::/64"}`,
		"too small":  `{"name":"bad","cidr":"10.20.0.0/26"}`,
		"bad name":   `{"name":"Bad Name","cidr":"10.30.0.0/16"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := request(t, h, http.MethodPost, "/v1/networks", body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestDuplicateNetworkNameIsRejected(t *testing.T) {
	h, _ := newTestModule(t)

	body := `{"name":"prod","cidr":"10.30.0.0/16"}`
	request(t, h, http.MethodPost, "/v1/networks", body)
	rec := request(t, h, http.MethodPost, "/v1/networks", body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestEachNodeGetsItsOwnSlice(t *testing.T) {
	_, m := newTestModule(t)
	ctx := context.Background()

	n, err := m.EnsureDefault(ctx, testProject)
	if err != nil {
		t.Fatalf("ensure default: %v", err)
	}

	first, err := m.SliceFor(ctx, n.ID, "n-1")
	if err != nil {
		t.Fatalf("slice for n-1: %v", err)
	}
	second, err := m.SliceFor(ctx, n.ID, "n-2")
	if err != nil {
		t.Fatalf("slice for n-2: %v", err)
	}

	if first.CIDR == second.CIDR {
		t.Fatalf("both nodes got %s; slices must not overlap", first.CIDR)
	}
	if first.CIDR != "10.20.0.64/26" {
		t.Fatalf("first slice = %q, want the second /26 because the first is reserved", first.CIDR)
	}
	if second.CIDR != "10.20.0.128/26" {
		t.Fatalf("second slice = %q, want the next /26", second.CIDR)
	}

	again, err := m.SliceFor(ctx, n.ID, "n-1")
	if err != nil {
		t.Fatalf("slice for n-1 again: %v", err)
	}
	if again.CIDR != first.CIDR {
		t.Fatalf("slice changed to %q on a second call, want %q", again.CIDR, first.CIDR)
	}
}

func TestAllocateGivesDistinctAddressesFromTheNodeSlice(t *testing.T) {
	h, m := newTestModule(t)
	ctx := context.Background()

	n, err := m.EnsureDefault(ctx, testProject)
	if err != nil {
		t.Fatalf("ensure default: %v", err)
	}

	for _, id := range []string{"i-1", "i-2", "i-3"} {
		if err := m.Allocate(ctx, id, n.ID, "n-1"); err != nil {
			t.Fatalf("allocate %s: %v", id, err)
		}
	}

	view := decode[nodeViewResponse](t, request(t, h, http.MethodGet, "/v1/nodes/n-1/network", ""))
	if len(view.Networks) != 1 {
		t.Fatalf("networks = %d, want 1", len(view.Networks))
	}

	entry := view.Networks[0]
	if entry.Slice != "10.20.0.64/26" {
		t.Fatalf("slice = %q, want the node slice", entry.Slice)
	}
	if entry.Gateway != "10.20.0.1" || entry.CIDR != DefaultCIDR {
		t.Fatalf("entry = %+v, want the network gateway and cidr", entry)
	}

	seen := map[string]bool{}
	for _, nic := range entry.NICs {
		if seen[nic.IP] {
			t.Fatalf("address %s was handed out twice", nic.IP)
		}
		seen[nic.IP] = true

		if !strings.HasPrefix(nic.IP, "10.20.0.") {
			t.Fatalf("ip = %q, want it inside the node slice", nic.IP)
		}
		if !strings.HasPrefix(nic.MAC, "02:") {
			t.Fatalf("mac = %q, want a locally administered address", nic.MAC)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("addresses = %d, want 3", len(seen))
	}
}

func TestAllocateIsIdempotentPerInstance(t *testing.T) {
	_, m := newTestModule(t)
	ctx := context.Background()

	n, _ := m.EnsureDefault(ctx, testProject)

	if err := m.Allocate(ctx, "i-1", n.ID, "n-1"); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if err := m.Allocate(ctx, "i-1", n.ID, "n-1"); err != nil {
		t.Fatalf("allocate again: %v", err)
	}

	nic, err := m.svc.repo.nic(ctx, "i-1")
	if err != nil {
		t.Fatalf("read nic: %v", err)
	}

	nics, err := m.svc.repo.nicsOnNode(ctx, "n-1")
	if err != nil {
		t.Fatalf("list nics: %v", err)
	}
	if len(nics) != 1 {
		t.Fatalf("nics = %d, want 1: a second call must not allocate again", len(nics))
	}
	if nics[0].IP != nic.IP {
		t.Fatalf("ip changed from %q to %q", nic.IP, nics[0].IP)
	}
}

func TestReleaseFreesTheAddressForReuse(t *testing.T) {
	_, m := newTestModule(t)
	ctx := context.Background()

	n, _ := m.EnsureDefault(ctx, testProject)
	if err := m.Allocate(ctx, "i-1", n.ID, "n-1"); err != nil {
		t.Fatalf("allocate: %v", err)
	}

	first, _ := m.svc.repo.nic(ctx, "i-1")

	if err := m.ReleaseAddress(ctx, "i-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := m.Allocate(ctx, "i-2", n.ID, "n-1"); err != nil {
		t.Fatalf("allocate after release: %v", err)
	}

	second, _ := m.svc.repo.nic(ctx, "i-2")
	if second.IP != first.IP {
		t.Fatalf("ip = %q, want the released %q to be reused", second.IP, first.IP)
	}
}

func TestNodeViewIsEmptyForAnUnknownNode(t *testing.T) {
	h, m := newTestModule(t)
	m.EnsureDefault(context.Background(), testProject)

	view := decode[nodeViewResponse](t, request(t, h, http.MethodGet, "/v1/nodes/n-nobody/network", ""))
	if len(view.Networks) != 0 {
		t.Fatalf("networks = %d, want 0", len(view.Networks))
	}
}

func TestAddressesDoNotCollideAcrossNodes(t *testing.T) {
	_, m := newTestModule(t)
	ctx := context.Background()

	n, _ := m.EnsureDefault(ctx, testProject)
	m.Allocate(ctx, "i-1", n.ID, "n-1")
	m.Allocate(ctx, "i-2", n.ID, "n-2")

	first, _ := m.svc.repo.nic(ctx, "i-1")
	second, _ := m.svc.repo.nic(ctx, "i-2")

	if first.IP == second.IP {
		t.Fatalf("both instances got %s across different nodes", first.IP)
	}
}

func TestOverlappingRangesAreRejected(t *testing.T) {
	h, _ := newTestModule(t)

	if rec := request(t, h, http.MethodPost, "/v1/networks",
		`{"name":"prod","cidr":"10.40.0.0/16"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}

	cases := map[string]string{
		"identical":  `{"name":"a","cidr":"10.40.0.0/16"}`,
		"inside":     `{"name":"b","cidr":"10.40.5.0/24"}`,
		"containing": `{"name":"c","cidr":"10.40.0.0/12"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := request(t, h, http.MethodPost, "/v1/networks", body)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d: two networks sharing a range give a node two "+
					"bridges with the same gateway address (body: %s)",
					rec.Code, http.StatusConflict, rec.Body.String())
			}
		})
	}

	if rec := request(t, h, http.MethodPost, "/v1/networks",
		`{"name":"elsewhere","cidr":"10.50.0.0/16"}`); rec.Code != http.StatusCreated {
		t.Fatalf("a non overlapping range was rejected: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateWithoutACIDRCannotShadowTheDefault(t *testing.T) {
	h, m := newTestModule(t)

	if _, err := m.EnsureDefault(context.Background(), testProject); err != nil {
		t.Fatalf("ensure default: %v", err)
	}

	rec := request(t, h, http.MethodPost, "/v1/networks", `{"name":"staging"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: an omitted cidr falls back to the default range, so "+
			"this silently created a second network on top of the first (body: %s)",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestDeleteRefusesANetworkStillInUse(t *testing.T) {
	h, m := newTestModule(t)

	rec := request(t, h, http.MethodPost, "/v1/networks", `{"name":"prod","cidr":"10.60.0.0/16"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if err := m.Allocate(context.Background(), "i-1", created.ID, "n-1"); err != nil {
		t.Fatalf("allocate: %v", err)
	}

	if rec := request(t, h, http.MethodDelete, "/v1/networks/"+created.ID, ""); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: deleting a network under a running instance strands it",
			rec.Code, http.StatusConflict)
	}

	if err := m.ReleaseAddress(context.Background(), "i-1"); err != nil {
		t.Fatalf("release: %v", err)
	}

	if rec := request(t, h, http.MethodDelete, "/v1/networks/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec := request(t, h, http.MethodGet, "/v1/networks/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the network gone", rec.Code)
	}
}

func TestDeleteReportsAnUnknownNetwork(t *testing.T) {
	h, _ := newTestModule(t)

	if rec := request(t, h, http.MethodDelete, "/v1/networks/nw-nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
