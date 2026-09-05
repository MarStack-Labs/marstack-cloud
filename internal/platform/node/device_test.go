package node

import (
	"context"
	"io"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newDeviceModule(t *testing.T) *Module {
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
	return m
}

func TestOnlyOneClaimLandsOnACard(t *testing.T) {
	m := newDeviceModule(t)
	ctx := context.Background()

	if err := m.svc.setDevices(ctx, "n-1", []Device{
		{Address: "0000:01:00.0", Kind: "gpu", Driver: "vfio-pci", Ready: true},
	}); err != nil {
		t.Fatalf("set devices: %v", err)
	}

	first, err := m.svc.repo.claimDevice(ctx, "n-1", "0000:01:00.0", "i-1")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := m.svc.repo.claimDevice(ctx, "n-1", "0000:01:00.0", "i-2")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}

	if !first || second {
		t.Fatalf("first=%v second=%v, want only the first. Claiming is read-then-write, so "+
			"the update has to be the one that decides: without instance_id = '' in its "+
			"WHERE, the second workload takes the card from under the first and both "+
			"guests try to open the same pci device", first, second)
	}
}

func TestAFreeCardIsOnlyOfferedWhileItIsFreeAndBound(t *testing.T) {
	m := newDeviceModule(t)
	ctx := context.Background()

	if err := m.svc.setDevices(ctx, "n-1", []Device{
		{Address: "0000:01:00.0", Kind: "gpu", Driver: "nvidia", Ready: false},
		{Address: "0000:02:00.0", Kind: "gpu", Driver: "vfio-pci", Ready: true},
	}); err != nil {
		t.Fatalf("set devices: %v", err)
	}

	address, found, err := m.svc.FreeDevice(ctx, "n-1", "gpu")
	if err != nil {
		t.Fatalf("free: %v", err)
	}
	if !found || address != "0000:02:00.0" {
		t.Fatalf("offered %q (found=%v), want the vfio one: a card the host driver still "+
			"holds cannot be opened by qemu", address, found)
	}

	if _, err := m.svc.ClaimDevice(ctx, "n-1", "gpu", "i-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, found, err := m.svc.FreeDevice(ctx, "n-1", "gpu"); err != nil || found {
		t.Fatalf("found=%v err=%v, want nothing free: the only bound card is taken, and "+
			"offering it again sends the scheduler to a node whose claim will fail",
			found, err)
	}
}

func TestACardAWorkloadHoldsDoesNotVanishFromTheInventory(t *testing.T) {
	m := newDeviceModule(t)
	ctx := context.Background()

	if err := m.svc.setDevices(ctx, "n-1", []Device{
		{Address: "0000:01:00.0", Kind: "gpu", Driver: "vfio-pci", Ready: true},
		{Address: "0000:02:00.0", Kind: "gpu", Driver: "vfio-pci", Ready: true},
	}); err != nil {
		t.Fatalf("set devices: %v", err)
	}
	if _, err := m.svc.ClaimDevice(ctx, "n-1", "gpu", "i-1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := m.svc.setDevices(ctx, "n-1", nil); err != nil {
		t.Fatalf("report nothing: %v", err)
	}

	held, err := m.svc.devices(ctx)
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("devices = %+v, want the claimed one kept. A card that disappears from "+
			"the inventory while a guest still has it leaves a workload holding something "+
			"nothing can account for", held)
	}
	if held[0].InstanceID != "i-1" {
		t.Fatalf("device = %+v, want the claim intact", held[0])
	}
	if held[0].Ready || held[0].Driver != "missing" {
		t.Fatalf("device = %+v, want it marked missing so an operator can see the card "+
			"their workload thinks it has is gone", held[0])
	}
}
