package volume

import (
	"context"
	"testing"
	"time"
)

func ourVM(nodeID string) fakeInstances {
	return fakeInstances{placements: map[string]Placement{
		"i-1": {ProjectID: "prj-default", NodeID: nodeID, Isolation: "vm"},
	}}
}

func scheduledVolume(t *testing.T, m *Module, at *time.Time) string {
	t.Helper()

	m.svc.now = func() time.Time { return *at }

	v, err := m.svc.create(context.Background(), CreateParams{
		ProjectID: "prj-default", Name: "data", SizeGiB: 1,
	})
	if err != nil {
		t.Fatalf("create volume: %v", err)
	}

	if _, err := m.svc.attach(context.Background(), v.ID, "prj-default", "i-1"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := m.svc.detach(context.Background(), v.ID, "prj-default"); err != nil {
		t.Fatalf("detach: %v", err)
	}

	if _, err := m.svc.setSchedule(context.Background(), ScheduleParams{
		ProjectID: "prj-default", VolumeID: v.ID, Every: "1h", Keep: 2,
	}); err != nil {
		t.Fatalf("set schedule: %v", err)
	}
	return v.ID
}

func TestASweepBeforeTheIntervalDoesNothing(t *testing.T) {
	_, m := newTestModule(t, ourVM("n-1"))

	at := time.Now().UTC()
	volume := scheduledVolume(t, m, &at)

	taken, _, err := m.svc.sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if taken != 0 {
		t.Fatalf("taken = %d, want none an hour early", taken)
	}

	snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
	if len(snaps) != 0 {
		t.Fatalf("snapshots = %d", len(snaps))
	}
}

func TestASweepAfterTheIntervalTakesOne(t *testing.T) {
	_, m := newTestModule(t, ourVM("n-1"))

	at := time.Now().UTC()
	volume := scheduledVolume(t, m, &at)

	at = at.Add(2 * time.Hour)
	taken, _, err := m.svc.sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if taken != 1 {
		t.Fatalf("taken = %d, want one once the interval has passed", taken)
	}

	snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want one", len(snaps))
	}
	if snaps[0].ScheduleID == "" {
		t.Fatal("the snapshot does not name the schedule that made it, so retention cannot " +
			"tell it from one somebody took by hand")
	}
}

func TestASweepWillNotStackOnAPendingSnapshot(t *testing.T) {
	_, m := newTestModule(t, ourVM("n-1"))

	at := time.Now().UTC()
	volume := scheduledVolume(t, m, &at)

	at = at.Add(2 * time.Hour)
	m.svc.sweep(context.Background())

	at = at.Add(2 * time.Hour)
	m.svc.sweep(context.Background())

	snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want the second sweep to skip while the first is still "+
			"pending on the node", len(snaps))
	}
}

func markEveryPendingReady(t *testing.T, m *Module, volume string) {
	t.Helper()

	snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
	for _, snap := range snaps {
		if snap.State != SnapshotPending {
			continue
		}
		if err := m.svc.repo.markSnapshot(
			context.Background(), snap.ID, SnapshotReady, "", 1024); err != nil {
			t.Fatalf("mark ready: %v", err)
		}
	}
}

func TestRetentionSettlesAndNeverGrows(t *testing.T) {
	_, m := newTestModule(t, ourVM("n-1"))

	at := time.Now().UTC()
	volume := scheduledVolume(t, m, &at)

	counts := make([]int, 0, 8)
	for range 8 {
		at = at.Add(2 * time.Hour)
		if _, _, err := m.svc.sweep(context.Background()); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		markEveryPendingReady(t, m, volume)

		snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
		counts = append(counts, len(snaps))
	}

	settled := counts[len(counts)-1]
	if settled > 3 {
		t.Fatalf("counts = %v: keep=2 must bound the set, and a schedule that grows without "+
			"bound fills the node it is protecting", counts)
	}
	for _, count := range counts[3:] {
		if count != settled {
			t.Fatalf("counts = %v, want a steady number once retention has caught up", counts)
		}
	}
}

func TestRetentionLeavesAPendingSnapshotAlone(t *testing.T) {
	_, m := newTestModule(t, ourVM("n-1"))

	at := time.Now().UTC()
	volume := scheduledVolume(t, m, &at)

	for range 3 {
		at = at.Add(2 * time.Hour)
		m.svc.sweep(context.Background())
		markEveryPendingReady(t, m, volume)
	}

	at = at.Add(2 * time.Hour)
	m.svc.sweep(context.Background())

	snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
	pending := 0
	for _, snap := range snaps {
		if snap.State == SnapshotPending {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want the newest one left alone: a snapshot the node has "+
			"not finished is not proven, so retention must not count or cut it", pending)
	}
}

func TestOnlyAutomaticSnapshotsArePruned(t *testing.T) {
	_, m := newTestModule(t, ourVM("n-1"))

	at := time.Now().UTC()
	volume := scheduledVolume(t, m, &at)

	byHand, err := m.svc.snapshot(context.Background(), volume, "prj-default", "keep-me")
	if err != nil {
		t.Fatalf("manual snapshot: %v", err)
	}
	m.svc.repo.markSnapshot(context.Background(), byHand.ID, SnapshotReady, "", 1024)

	for range 4 {
		at = at.Add(2 * time.Hour)
		m.svc.sweep(context.Background())

		snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
		for _, snap := range snaps {
			if snap.State == SnapshotPending {
				m.svc.repo.markSnapshot(context.Background(), snap.ID, SnapshotReady, "", 1024)
			}
		}
	}

	snaps, _ := m.svc.repo.snapshots(context.Background(), volume)
	for _, snap := range snaps {
		if snap.ID == byHand.ID {
			return
		}
	}
	t.Fatalf("the hand-made snapshot was pruned, and retention must only touch what the "+
		"schedule made: %d left", len(snaps))
}
