package logs

import (
	"context"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newRepo(t *testing.T) *repository {
	t.Helper()

	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/logs.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	module := New(st, logging.New("error", io.Discard))
	if err := st.Migrate(ctx, module.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return newRepository(st)
}

func fill(t *testing.T, repo *repository, instanceID string, from, count int) {
	t.Helper()

	texts := make([]string, 0, count)
	for i := range count {
		texts = append(texts, "line "+strconv.Itoa(from+i))
	}
	if err := repo.append(context.Background(), instanceID, texts, time.Now().UTC()); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func countOf(t *testing.T, repo *repository, instanceID string) int {
	t.Helper()

	var total int
	err := repo.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM instance_logs WHERE instance_id = ?`, instanceID).Scan(&total)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return total
}

func TestWhatIsKeptIsBoundedNoMatterHowMuchArrives(t *testing.T) {
	repo := newRepo(t)

	for batch := range 8 {
		fill(t, repo, "i-1", batch*MaxLinesPerReport, MaxLinesPerReport)
	}

	held := countOf(t, repo, "i-1")
	if held > MaxLinesPerInstance {
		t.Fatalf("held = %d, want at most %d: a workload printing in a loop must not fill "+
			"the control plane's disk, and the API cannot show this because tail caps "+
			"the answer long before the store does", held, MaxLinesPerInstance)
	}
	if held < MaxLinesPerInstance {
		t.Fatalf("held = %d, want the full window kept: trimming more than asked throws "+
			"away output somebody may still need", held)
	}
}

func TestTrimmingDropsTheOldestAndKeepsTheNewest(t *testing.T) {
	repo := newRepo(t)

	total := MaxLinesPerInstance + 500
	for from := 0; from < total; from += MaxLinesPerReport {
		fill(t, repo, "i-1", from, MaxLinesPerReport)
	}

	lines, err := repo.tail(context.Background(), "i-1", MaxLinesPerInstance)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if lines[0].Text != "line 500" {
		t.Fatalf("oldest kept = %q, want line 500", lines[0].Text)
	}
	if lines[len(lines)-1].Text != "line "+strconv.Itoa(total-1) {
		t.Fatalf("newest kept = %q, want line %d", lines[len(lines)-1].Text, total-1)
	}
}

func TestTrimmingLeavesOtherInstancesAlone(t *testing.T) {
	repo := newRepo(t)

	fill(t, repo, "i-quiet", 0, 3)
	for batch := range 6 {
		fill(t, repo, "i-loud", batch*MaxLinesPerReport, MaxLinesPerReport)
	}

	if held := countOf(t, repo, "i-quiet"); held != 3 {
		t.Fatalf("the quiet instance holds %d lines, want 3: the cap is per instance, and "+
			"trimming across all of them lets one workload erase another's output", held)
	}
}

func TestAShortLogIsNotTrimmedAtAll(t *testing.T) {
	repo := newRepo(t)
	fill(t, repo, "i-1", 0, 5)

	if held := countOf(t, repo, "i-1"); held != 5 {
		t.Fatalf("held = %d, want 5", held)
	}
}
