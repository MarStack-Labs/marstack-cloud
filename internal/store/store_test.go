package store

import (
	"context"
	"testing"
)

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	first.Close()

	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()

	var version int
	if err := second.DB().QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("schema_version = %d, want %d", version, len(migrations))
	}
}

func TestSchemaVersionHasSingleRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	for range 3 {
		st, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		st.Close()
	}

	st, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	var rows int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_version rows = %d, want 1", rows)
	}
}
