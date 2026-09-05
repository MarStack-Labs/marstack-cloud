package agent

import (
	"context"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

type releasingRuntime struct {
	fakeRuntime

	mu       sync.Mutex
	keep     []workload.Disk
	known    []workload.Disk
	calls    int
	released []workload.Released
}

func (r *releasingRuntime) ReleaseDisks(
	_ string, keep, known []workload.Disk,
) ([]workload.Released, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls++
	r.keep = append([]workload.Disk(nil), keep...)
	r.known = append([]workload.Disk(nil), known...)
	return r.released, nil
}

func (r *releasingRuntime) sawKeep() []workload.Disk {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keep
}

func newReleaseHarness(t *testing.T, volumes []volumeView,
	released []workload.Released) (*Agent, *controlPlane, *releasingRuntime) {
	t.Helper()

	in := runningInstance()
	in.Isolation = "vm"

	cp := &controlPlane{
		instances: []instanceView{in},
		networks:  []networkView{defaultNetworkView(in.ID, "10.20.0.65")},
		volumes:   volumes,
	}

	rt := &releasingRuntime{
		fakeRuntime: fakeRuntime{state: workload.State{Phase: workload.PhaseRunning}},
		released:    released,
	}

	srv := httptest.NewServer(cp.handler())
	t.Cleanup(srv.Close)

	a := New(Config{Endpoint: srv.URL, Name: "bm-1", Interval: time.Hour},
		Deps{Runtimes: map[string]workload.Runtime{"vm": rt}, Datapath: &fakeDatapath{}},
		logging.New("error", io.Discard))

	if err := a.register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	return a, cp, rt
}

func TestADetachingDiskIsNotInWhatTheGuestShouldKeep(t *testing.T) {
	in := runningInstance()
	a, _, rt := newReleaseHarness(t, []volumeView{
		{ID: "vol-stay", Name: "data", SizeGiB: 20, InstanceID: in.ID},
		{ID: "vol-go", Name: "spare", SizeGiB: 5, InstanceID: in.ID, Detaching: true},
	}, []workload.Released{{VolumeID: "vol-go", Gone: true}})

	a.reconcile(context.Background())

	if rt.calls == 0 {
		t.Fatal("the guest was never asked to release anything")
	}

	keep := rt.sawKeep()
	for _, disk := range keep {
		if disk.ID == "vol-go" {
			t.Fatalf("keep = %+v, still holds the detaching disk. The runtime unplugs what "+
				"is attached and not in this list, so leaving it here means the guest is "+
				"never asked and the volume stays detaching forever", keep)
		}
	}
	if len(keep) != 1 || keep[0].ID != "vol-stay" {
		t.Fatalf("keep = %+v, want the disk that is staying", keep)
	}
}

func TestAReleasedDiskIsReportedBack(t *testing.T) {
	in := runningInstance()
	a, cp, _ := newReleaseHarness(t, []volumeView{
		{ID: "vol-go", Name: "spare", SizeGiB: 5, InstanceID: in.ID, Detaching: true},
	}, []workload.Released{{VolumeID: "vol-go", Gone: true}})

	a.reconcile(context.Background())

	held := cp.volumeReports()
	if len(held) != 1 {
		t.Fatalf("reports = %+v, want the release reported: the control plane cannot see a "+
			"guest, so a disk released and never mentioned leaves the volume detaching "+
			"forever", held)
	}
	if held[0].VolumeID != "vol-go" || !held[0].Detached {
		t.Fatalf("report = %+v, want vol-go detached", held[0])
	}
}

func TestAGuestRefusingIsReportedAsAReason(t *testing.T) {
	in := runningInstance()
	a, cp, _ := newReleaseHarness(t, []volumeView{
		{ID: "vol-go", Name: "spare", SizeGiB: 5, InstanceID: in.ID, Detaching: true},
	}, []workload.Released{{VolumeID: "vol-go", Reason: "the guest still holds it"}})

	a.reconcile(context.Background())

	held := cp.volumeReports()
	if len(held) != 1 {
		t.Fatalf("reports = %+v, want the refusal reported", held)
	}
	if held[0].Detached {
		t.Fatal("a disk the guest would not release was reported as detached, which would " +
			"let it be attached somewhere else while it is still being written to")
	}
	if held[0].Error == "" {
		t.Fatal("the refusal carried no reason, so an operator has nothing to act on")
	}
}

func TestNothingIsAskedWhenNoDiskIsLeaving(t *testing.T) {
	in := runningInstance()
	a, _, rt := newReleaseHarness(t, []volumeView{
		{ID: "vol-stay", Name: "data", SizeGiB: 20, InstanceID: in.ID},
	}, nil)

	a.reconcile(context.Background())

	if rt.calls != 0 {
		t.Fatalf("the guest was asked %d times with nothing detaching. Every pass would open "+
			"a QMP connection to every running guest for nothing", rt.calls)
	}
}
