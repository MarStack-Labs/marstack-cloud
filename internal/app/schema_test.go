package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

var (
	schemaOnce   sync.Once
	schemaSource string
	schemaErr    error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if schemaSource != "" {
		os.RemoveAll(schemaSource)
	}
	os.Exit(code)
}

func schemaDir(t *testing.T) string {
	t.Helper()

	schemaOnce.Do(buildSchemaTemplate)
	if schemaErr != nil {
		t.Fatalf("build the schema template: %v", schemaErr)
	}

	dir := t.TempDir()
	entries, err := os.ReadDir(schemaSource)
	if err != nil {
		t.Fatalf("read the schema template: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(schemaSource, e.Name()))
		if err != nil {
			t.Fatalf("read %s from the schema template: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), body, 0o600); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	return dir
}

func buildSchemaTemplate() {
	ctx := context.Background()
	log := logging.New("error", io.Discard)

	probe, err := os.MkdirTemp("", "marstack-probe-")
	if err != nil {
		schemaErr = err
		return
	}
	defer os.RemoveAll(probe)

	a, err := New(ctx, Config{DataDir: probe, RatePerSecond: &unlimited}, log)
	if err != nil {
		schemaErr = err
		return
	}

	var all []store.Migration
	for _, m := range a.modules {
		all = append(all, m.Migrations()...)
	}
	a.Close()

	template, err := os.MkdirTemp("", "marstack-schema-")
	if err != nil {
		schemaErr = err
		return
	}

	st, err := store.Open(ctx, template)
	if err != nil {
		schemaErr = err
		return
	}
	if err := st.Migrate(ctx, all); err != nil {
		st.Close()
		schemaErr = err
		return
	}
	if err := st.Close(); err != nil {
		schemaErr = err
		return
	}

	schemaSource = template
}

func TestTheSchemaTemplateCarriesNoRowsOfItsOwn(t *testing.T) {
	dir := schemaDir(t)

	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open the copy: %v", err)
	}
	defer st.Close()

	for _, table := range []string{"projects", "networks", "tokens", "instances"} {
		var count int
		if err := st.DB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s holds %d rows in the template, so every test would start from "+
				"somebody else's state", table, count)
		}
	}
}
