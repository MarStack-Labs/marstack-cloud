package token

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	m := New(st, logging.New("error", io.Discard), nil)
	m.UseProjects(knownProjects{defaultProjectID: true})
	if err := st.Migrate(ctx, m.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return m, dir
}

type knownProjects map[string]bool

func (k knownProjects) Exists(_ context.Context, id string) (bool, error) {
	return k[id], nil
}

func TestATokenCannotBePlacedInAProjectThatDoesNotExist(t *testing.T) {
	m, _ := newTestModule(t)

	_, _, err := m.svc.create(context.Background(),
		CreateParams{Name: "stray", Role: RoleMember, ProjectID: "prj-nope"})
	if err == nil {
		t.Fatal("the token was created, so it would authorise work in a project nobody can see")
	}
}

func TestATokenWithoutAProjectLandsInTheDefaultOne(t *testing.T) {
	m, _ := newTestModule(t)

	created, _, err := m.svc.create(context.Background(),
		CreateParams{Name: "unplaced", Role: RoleMember})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ProjectID != defaultProjectID {
		t.Fatalf("project = %q, want the default project", created.ProjectID)
	}
}

func TestTwoProjectsMayEachHaveATokenOfTheSameName(t *testing.T) {
	m, _ := newTestModule(t)
	m.UseProjects(knownProjects{defaultProjectID: true, "prj-other": true})
	ctx := context.Background()

	if _, _, err := m.svc.create(ctx, CreateParams{Name: "ci", Role: RoleMember}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, _, err := m.svc.create(ctx,
		CreateParams{Name: "ci", Role: RoleMember, ProjectID: "prj-other"}); err != nil {
		t.Fatalf("second: %v: a name is only taken inside its own project", err)
	}
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

func TestALifetimeIsReadInDaysAndWeeksAsWellAsHours(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"":    0,
		"12h": 12 * time.Hour,
		"90m": 90 * time.Minute,
		"30d": 30 * 24 * time.Hour,
		"4w":  4 * 7 * 24 * time.Hour,
	} {
		got, err := ParseLifetime(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if got != want {
			t.Fatalf("%q = %v, want %v", text, got, want)
		}
	}

	for _, text := range []string{"soon", "30 days", "1y", "d"} {
		if _, err := ParseLifetime(text); err == nil {
			t.Fatalf("%q was accepted", text)
		}
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	m, _ := newTestModule(t)
	ctx := context.Background()

	now := time.Now().UTC()
	m.svc.now = func() time.Time { return now }

	_, secret, err := m.svc.create(ctx,
		CreateParams{Name: "short", Role: RoleMember, ExpiresIn: "1h"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := m.Verify(ctx, secret); err != nil {
		t.Fatalf("the token was refused while it was still valid: %v", err)
	}

	m.svc.now = func() time.Time { return now.Add(time.Hour + time.Second) }
	if _, err := m.Verify(ctx, secret); err == nil {
		t.Fatal("an expired token still authenticated")
	}
}

func TestATokenWithNoLifetimeNeverExpires(t *testing.T) {
	m, _ := newTestModule(t)
	ctx := context.Background()

	_, secret, err := m.svc.create(ctx, CreateParams{Name: "forever", Role: RoleMember})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	m.svc.now = func() time.Time { return time.Now().UTC().Add(100 * 365 * 24 * time.Hour) }
	if _, err := m.Verify(ctx, secret); err != nil {
		t.Fatalf("a token without a lifetime expired anyway: %v", err)
	}
}

func TestALifetimeInThePastIsRefused(t *testing.T) {
	m, _ := newTestModule(t)

	if _, _, err := m.svc.create(context.Background(),
		CreateParams{Name: "born-dead", Role: RoleMember, ExpiresIn: "-1h"}); err == nil {
		t.Fatal("a token that was already expired was created")
	}
}

func TestAnExpiredAdminDoesNotCountAsTheLastOne(t *testing.T) {
	m, dir := newTestModule(t)
	ctx := context.Background()

	now := time.Now().UTC()
	m.svc.now = func() time.Time { return now }
	if err := m.EnsureBootstrap(ctx, dir); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	stale, _, err := m.svc.create(ctx, CreateParams{Name: "stale", Role: RoleAdmin, ExpiresIn: "1h"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	m.svc.now = func() time.Time { return now.Add(2 * time.Hour) }

	tokens, err := m.svc.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var bootstrapID string
	for _, one := range tokens {
		if one.Name == BootstrapName {
			bootstrapID = one.ID
		}
	}

	if err := m.svc.remove(ctx, bootstrapID); err == nil {
		t.Fatalf("the only admin token that still works was revoked because an expired one "+
			"made the count look like two (stale = %s)", stale.ID)
	}
}
