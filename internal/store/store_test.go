package store

import (
	"context"
	"testing"
)

func testMigrations() []Migration {
	return []Migration{
		{Module: "demo", Index: 1, SQL: `CREATE TABLE demo (id TEXT PRIMARY KEY)`},
		{Module: "demo", Index: 2, SQL: `CREATE INDEX demo_id ON demo (id)`},
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	for range 3 {
		st, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := st.Migrate(ctx, testMigrations()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		st.Close()
	}

	st, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	var applied int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(testMigrations()) {
		t.Fatalf("applied migrations = %d, want %d", applied, len(testMigrations()))
	}
}

func TestMigrateRecordsEachModuleSeparately(t *testing.T) {
	ctx := context.Background()

	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	migrations := append(testMigrations(), Migration{
		Module: "other",
		Index:  1,
		SQL:    `CREATE TABLE other (id TEXT PRIMARY KEY)`,
	})
	if err := st.Migrate(ctx, migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var modules int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT module) FROM schema_migrations`,
	).Scan(&modules); err != nil {
		t.Fatalf("count modules: %v", err)
	}
	if modules != 2 {
		t.Fatalf("distinct modules = %d, want 2", modules)
	}
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	ctx := context.Background()

	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	broken := []Migration{{Module: "demo", Index: 1, SQL: `CREATE TABLE ( invalid sql`}}
	if err := st.Migrate(ctx, broken); err == nil {
		t.Fatal("expected migration to fail")
	}

	var recorded int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if recorded != 0 {
		t.Fatalf("recorded migrations = %d, want 0", recorded)
	}
}

func TestABrokenMigrationTakesTheOnesBeforeItWithIt(t *testing.T) {
	ctx := context.Background()

	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	batch := []Migration{
		{Module: "demo", Index: 1, SQL: `CREATE TABLE early (id TEXT PRIMARY KEY)`},
		{Module: "demo", Index: 2, SQL: `CREATE TABLE ( invalid sql`},
	}
	if err := st.Migrate(ctx, batch); err == nil {
		t.Fatal("a batch holding invalid sql was applied")
	}

	var tables int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'early'`,
	).Scan(&tables); err != nil {
		t.Fatalf("look for the early table: %v", err)
	}
	if tables != 0 {
		t.Fatal("the first migration survived a batch that failed, so an upgrade that " +
			"stops halfway leaves a schema that is neither the old one nor the new one")
	}

	var recorded int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if recorded != 0 {
		t.Fatalf("recorded migrations = %d, want 0", recorded)
	}
}

func TestMigrateAppliesOnlyWhatIsMissing(t *testing.T) {
	ctx := context.Background()

	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	first := []Migration{{Module: "demo", Index: 1, SQL: `CREATE TABLE one (id TEXT)`}}
	if err := st.Migrate(ctx, first); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	both := append(first, Migration{Module: "demo", Index: 2, SQL: `CREATE TABLE two (id TEXT)`})
	if err := st.Migrate(ctx, both); err != nil {
		t.Fatalf("second migrate: %v: an already applied migration must not be run again", err)
	}

	var recorded int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if recorded != 2 {
		t.Fatalf("recorded migrations = %d, want 2", recorded)
	}
}
