package token

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newTestModule(t *testing.T) (*Module, string) {
	t.Helper()

	ctx := context.Background()
	dir := t.TempDir()

	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := New(st, logging.New("error", io.Discard))
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return m, dir
}

func TestBootstrapHappensOnceAndLandsInAFile(t *testing.T) {
	m, dir := newTestModule(t)
	ctx := context.Background()

	if err := m.EnsureBootstrap(ctx, dir); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	path := filepath.Join(dir, BootstrapFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600: the file is a credential", info.Mode().Perm())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	secret := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(secret, "mst_") {
		t.Fatalf("secret = %q", secret)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := m.EnsureBootstrap(ctx, dir); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a second bootstrap minted another token, so restarting would hand out admin rights")
	}

	identity, err := m.Verify(ctx, secret)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if identity.Role != RoleAdmin || identity.Name != BootstrapName {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestVerifyRefusesWhatIsNotAToken(t *testing.T) {
	m, _ := newTestModule(t)
	ctx := context.Background()

	if err := m.EnsureBootstrap(ctx, t.TempDir()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	for name, secret := range map[string]string{
		"empty":      "",
		"whitespace": "   ",
		"invented":   "mst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := m.Verify(ctx, secret); err == nil {
				t.Fatalf("%q was accepted", secret)
			}
		})
	}
}

func TestSecretsAreStoredHashed(t *testing.T) {
	m, dir := newTestModule(t)
	ctx := context.Background()

	if err := m.EnsureBootstrap(ctx, dir); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, BootstrapFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	secret := strings.TrimSpace(string(raw))

	database, err := os.ReadFile(filepath.Join(dir, "marstack.db"))
	if err != nil {
		t.Skipf("cannot read the database file: %v", err)
	}
	if strings.Contains(string(database), secret) {
		t.Fatal("the token secret is in the database in the clear")
	}
}

func TestTwoTokensNeverShareASecret(t *testing.T) {
	m, _ := newTestModule(t)
	ctx := context.Background()

	seen := map[string]bool{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		_, secret, err := m.svc.create(ctx, CreateParams{Name: name, Role: RoleNode})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if seen[secret] {
			t.Fatal("two tokens got the same secret")
		}
		seen[secret] = true
	}
}
