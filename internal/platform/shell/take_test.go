package shell

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newTestRepo(t *testing.T) *repository {
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
	return m.svc.repo
}

func TestOnlyOneTakerWinsEvenAfterTheyAllRead(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	held := Session{
		ID: "sh-1", ProjectID: "prj-default", InstanceID: "i-1", NodeID: "n-1",
		Isolation: "container", Command: []string{"/bin/sh"},
		State: StateWaiting, CreatedAt: at,
	}
	if err := repo.insert(ctx, held); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for range 8 {
		found, err := repo.nextWaitingOn(ctx, "n-1")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if found.ID != held.ID {
			t.Fatalf("read %q, want %q", found.ID, held.ID)
		}
	}

	won := 0
	for range 8 {
		taken, err := repo.take(ctx, held.ID, at)
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		if taken {
			won++
		}
	}

	if won != 1 {
		t.Fatalf("%d takers won after all of them had read the row. Handing a session out "+
			"is two steps, so the update has to be the one that decides: without "+
			"state = 'waiting' in its WHERE, every node that read it thinks it owns the "+
			"shell and they all write into one stream", won)
	}
}
