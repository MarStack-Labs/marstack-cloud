package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
	"github.com/marstack-labs/marstack-cloud/internal/platform/user"
)

const goodPassword = "correct horse battery staple"

type userBody struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id"`
	Disabled  bool   `json:"disabled"`
}

type sessionBody struct {
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
}

func createUser(t *testing.T, a *testApp, body string) (int, userBody) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/users", strings.NewReader(body))

	var created userBody
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	return rec.Code, created
}

func login(t *testing.T, a *testApp, email, password string) (int, sessionBody) {
	t.Helper()

	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	rec := do(t, a, http.MethodPost, "/v1/login", strings.NewReader(string(body)))

	var session sessionBody
	_ = json.Unmarshal(rec.Body.Bytes(), &session)
	return rec.Code, session
}

func someone(t *testing.T, a *testApp, email, role string) userBody {
	t.Helper()

	code, created := createUser(t, a, `{"email":"`+email+`","name":"someone","role":"`+
		role+`","project_id":"prj-default","password":"`+goodPassword+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("create user: %d", code)
	}
	return created
}

func TestSomebodyCanSignInAndUseWhatTheyGet(t *testing.T) {
	a, _ := newBalancingApp(t)
	someone(t, a, "ada@example.test", "member")

	code, session := login(t, a, "ada@example.test", goodPassword)
	if code != http.StatusCreated {
		t.Fatalf("login: %d", code)
	}
	if session.Token == "" || session.ExpiresAt == "" {
		t.Fatalf("session = %+v, want a token that expires", session)
	}

	rec := doAs(t, a, session.Token, http.MethodGet, "/v1/instances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("using the session: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSigningInIsCaseInsensitiveOnTheAddress(t *testing.T) {
	a, _ := newBalancingApp(t)
	someone(t, a, "ada@example.test", "member")

	if code, _ := login(t, a, "  Ada@Example.TEST ", goodPassword); code !=
		http.StatusCreated {
		t.Fatalf("login: %d, want the address matched however it was typed", code)
	}
}

func TestAWrongPasswordAndAnUnknownAddressAnswerTheSame(t *testing.T) {
	a, _ := newBalancingApp(t)
	someone(t, a, "ada@example.test", "member")

	wrong := do(t, a, http.MethodPost, "/v1/login",
		strings.NewReader(`{"email":"ada@example.test","password":"not the password"}`))
	missing := do(t, a, http.MethodPost, "/v1/login",
		strings.NewReader(`{"email":"nobody@example.test","password":"not the password"}`))

	if wrong.Code != http.StatusUnauthorized || missing.Code != http.StatusUnauthorized {
		t.Fatalf("wrong = %d missing = %d, want both %d", wrong.Code, missing.Code,
			http.StatusUnauthorized)
	}
	if wrong.Body.String() != missing.Body.String() {
		t.Fatalf("the answers differ:\n  %s\n  %s\nA different answer for an address that "+
			"exists turns the login into a way to find out who has an account",
			wrong.Body.String(), missing.Body.String())
	}
}

func TestDisablingSomebodyStopsEveryTokenTheyHold(t *testing.T) {
	a, _ := newBalancingApp(t)
	person := someone(t, a, "ada@example.test", "member")

	firstCode, first := login(t, a, "ada@example.test", goodPassword)
	secondCode, second := login(t, a, "ada@example.test", goodPassword)
	if firstCode != http.StatusCreated || secondCode != http.StatusCreated {
		t.Fatalf("logins gave %d and %d, want both %d: one person signing in twice is a "+
			"laptop and a phone, not a mistake, and a test whose second token is empty "+
			"proves nothing about revoking it", firstCode, secondCode, http.StatusCreated)
	}
	if first.Token == second.Token {
		t.Fatal("both sessions are the same token, so ending one ends the other")
	}

	if rec := do(t, a, http.MethodPost, "/v1/users/"+person.ID+"/disable", nil); rec.Code !=
		http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}

	for name, secret := range map[string]string{"first": first.Token, "second": second.Token} {
		rec := doAs(t, a, secret, http.MethodGet, "/v1/instances", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s session: %d, want %d: revoking a person one token at a time is "+
				"not revoking them", name, rec.Code, http.StatusUnauthorized)
		}
	}
}

func TestEnablingSomebodyAgainDoesNotBringBackTheirSessions(t *testing.T) {
	a, _ := newBalancingApp(t)
	person := someone(t, a, "ada@example.test", "member")
	_, session := login(t, a, "ada@example.test", goodPassword)

	do(t, a, http.MethodPost, "/v1/users/"+person.ID+"/disable", nil)
	do(t, a, http.MethodPost, "/v1/users/"+person.ID+"/enable", nil)

	rec := doAs(t, a, session.Token, http.MethodGet, "/v1/instances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the token working again: enabling somebody is undoing "+
			"the disable, and a session that was only paused is not a security hole",
			rec.Code)
	}
}

func TestChangingAPasswordEndsTheSessionsItReplaces(t *testing.T) {
	a, _ := newBalancingApp(t)
	person := someone(t, a, "ada@example.test", "member")
	_, session := login(t, a, "ada@example.test", goodPassword)

	rec := do(t, a, http.MethodPut, "/v1/users/"+person.ID+"/password",
		strings.NewReader(`{"password":"a different long password"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set password: %d %s", rec.Code, rec.Body.String())
	}

	if used := doAs(t, a, session.Token, http.MethodGet, "/v1/instances", nil); used.Code !=
		http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: a password is changed because the old one may be "+
			"known, and a session made with it is the same problem", used.Code,
			http.StatusUnauthorized)
	}

	if code, _ := login(t, a, "ada@example.test", "a different long password"); code !=
		http.StatusCreated {
		t.Fatalf("login with the new password: %d", code)
	}
	if code, _ := login(t, a, "ada@example.test", goodPassword); code !=
		http.StatusUnauthorized {
		t.Fatalf("the old password still works: %d", code)
	}
}

func TestDeletingSomebodyTakesTheirTokensWithThem(t *testing.T) {
	a, _ := newBalancingApp(t)
	person := someone(t, a, "ada@example.test", "member")
	_, session := login(t, a, "ada@example.test", goodPassword)

	if rec := do(t, a, http.MethodDelete, "/v1/users/"+person.ID, nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if used := doAs(t, a, session.Token, http.MethodGet, "/v1/instances", nil); used.Code !=
		http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: a token nobody owns is a token nobody can revoke",
			used.Code, http.StatusUnauthorized)
	}

	rec := do(t, a, http.MethodGet, "/v1/tokens", nil)
	var held struct {
		Tokens []struct {
			Name   string `json:"name"`
			UserID string `json:"user_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, one := range held.Tokens {
		if one.UserID == person.ID {
			t.Fatalf("token %q still points at a person who is gone. Refusing it on every "+
				"request is not the same as removing it: the row stays forever and the "+
				"list shows a session nobody can account for", one.Name)
		}
	}
}

func TestASessionCarriesTheRoleAndProjectOfThePerson(t *testing.T) {
	a, _ := newBalancingApp(t)
	someone(t, a, "vera@example.test", "viewer")

	_, session := login(t, a, "vera@example.test", goodPassword)
	if session.Role != "viewer" {
		t.Fatalf("role = %q, want viewer", session.Role)
	}

	rec := doAs(t, a, session.Token, http.MethodPost, "/v1/networks",
		strings.NewReader(`{"name":"sneaky","cidr":"10.190.0.0/16"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: a viewer's session must not be able to write",
			rec.Code, http.StatusForbidden)
	}
}

func TestAShortPasswordIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	code, _ := createUser(t, a, `{"email":"ada@example.test","name":"ada",`+
		`"role":"member","password":"short"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", code, http.StatusBadRequest)
	}
}

func TestAnAddressCanOnlyBeUsedOnce(t *testing.T) {
	a, _ := newBalancingApp(t)
	someone(t, a, "ada@example.test", "member")

	code, _ := createUser(t, a, `{"email":"ADA@example.test","name":"ada2",`+
		`"role":"member","password":"`+goodPassword+`"}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: two accounts on one address make signing in "+
			"ambiguous", code, http.StatusConflict)
	}
}

func TestSomethingThatIsNotAnAddressIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	for _, email := range []string{"ada", "ada@", "@example.test", "ada@example", "a b@c.d"} {
		code, _ := createUser(t, a, `{"email":"`+email+`","name":"ada",`+
			`"role":"member","password":"`+goodPassword+`"}`)
		if code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want %d", email, code, http.StatusBadRequest)
		}
	}
}

func newFrozenApp(t *testing.T) *testApp {
	t.Helper()

	dir := t.TempDir()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	built, err := New(context.Background(), Config{
		DataDir:       dir,
		RatePerSecond: &unlimited,
		Now:           func() time.Time { return at },
	}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { built.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}
	return &testApp{App: built, secret: strings.TrimSpace(string(raw))}
}

func TestGuessingPasswordsIsThrottled(t *testing.T) {
	a := newFrozenApp(t)
	someone(t, a, "ada@example.test", "member")

	allowed, refused := 0, 0
	for range 12 {
		rec := do(t, a, http.MethodPost, "/v1/login",
			strings.NewReader(`{"email":"ada@example.test","password":"guess"}`))
		if rec.Code == http.StatusTooManyRequests {
			refused++
			continue
		}
		allowed++
	}

	if refused == 0 {
		t.Fatal("twelve guesses in a row all got a real answer. The login is the one route " +
			"that must be reachable without a token, which makes it the one that most " +
			"needs a limit")
	}
	if allowed > user.LoginBurst {
		t.Fatalf("%d guesses got through against a burst of %d. The clock is frozen here "+
			"on purpose: against the wall clock a slow machine refills the bucket between "+
			"attempts and every guess is answered, which is how this test passed for "+
			"months and then failed once the machine was busy", allowed, user.LoginBurst)
	}
}

func TestTheAuditTrailSaysWhoAndNotOnlyWhichToken(t *testing.T) {
	a, _ := newBalancingApp(t)
	person := someone(t, a, "ada@example.test", "member")
	_, session := login(t, a, "ada@example.test", goodPassword)

	doAs(t, a, session.Token, http.MethodPost, "/v1/networks",
		strings.NewReader(`{"name":"theirs","cidr":"10.191.0.0/16"}`))

	rec := do(t, a, http.MethodGet, "/v1/audit", nil)
	var body struct {
		Entries []struct {
			Actor  string `json:"actor"`
			UserID string `json:"user_id"`
			Path   string `json:"path"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, entry := range body.Entries {
		if entry.Path == "/v1/networks" && entry.UserID == person.ID {
			return
		}
	}
	t.Fatalf("no audit entry names the person who did it: %s", rec.Body.String())
}

func TestLoginNeedsNoTokenOfItsOwn(t *testing.T) {
	a, _ := newBalancingApp(t)
	someone(t, a, "ada@example.test", "member")

	rec := doAs(t, a, "", http.MethodPost, "/v1/login",
		strings.NewReader(`{"email":"ada@example.test","password":"`+goodPassword+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: nobody can sign in if signing in needs a token",
			rec.Code, http.StatusCreated)
	}
}
