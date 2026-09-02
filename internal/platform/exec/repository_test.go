package exec

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newExecRepo(t *testing.T) (*Module, *repository) {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/exec.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard))
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return m, newRepository(st)
}

func TestOnlyOneTakerWinsARace(t *testing.T) {
	m, repo := newExecRepo(t)
	ctx := context.Background()

	if err := repo.insert(ctx, Run{
		ID: "ex-1", ProjectID: "prj-test", InstanceID: "i-1", NodeID: "n-1",
		Isolation: "container", Command: []string{"true"}, Timeout: time.Second,
		State: StateWaiting, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		won int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, found, err := m.svc.take(ctx, "n-1"); err == nil && found {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if won != 1 {
		t.Fatalf("%d takers were given the same command. Reading it and marking it taken "+
			"are two steps, so the update has to be the one that decides: without the "+
			"condition on it every taker that read the row runs the command", won)
	}
}
