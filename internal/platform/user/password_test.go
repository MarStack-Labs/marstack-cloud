package user

import (
	"strings"
	"testing"
)

func TestTheSamePasswordHashesDifferentlyEveryTime(t *testing.T) {
	first, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	if first == second {
		t.Fatal("two hashes of one password are identical, so there is no salt and one " +
			"stolen table tells you which accounts share a password")
	}
}

func TestThePasswordIsNotInWhatIsStored(t *testing.T) {
	password := "correct horse battery staple"

	stored, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if strings.Contains(stored, password) {
		t.Fatalf("stored = %q, and the password is in it", stored)
	}
	if !strings.HasPrefix(stored, scheme+"$") {
		t.Fatalf("stored = %q, want the scheme and cost on the front so an old hash can "+
			"still be read after either changes", stored)
	}
}

func TestTheRightPasswordMatchesAndOthersDoNot(t *testing.T) {
	stored, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	same, err := matches(stored, "correct horse battery staple")
	if err != nil || !same {
		t.Fatalf("same = %v err = %v, want the right password accepted", same, err)
	}

	for _, wrong := range []string{
		"correct horse battery stapl",
		"correct horse battery staple ",
		"",
		"Correct Horse Battery Staple",
	} {
		same, err := matches(stored, wrong)
		if err != nil {
			t.Fatalf("%q: %v", wrong, err)
		}
		if same {
			t.Fatalf("%q was accepted", wrong)
		}
	}
}

func TestAStoredValueThatMakesNoSenseIsRefused(t *testing.T) {
	for _, stored := range []string{
		"",
		"plaintext",
		"pbkdf2-sha256$notanumber$c2FsdA$aGFzaA",
		"md5$1$c2FsdA$aGFzaA",
		"pbkdf2-sha256$600000$c2FsdA",
	} {
		if _, err := matches(stored, "anything"); err == nil {
			t.Fatalf("%q was read as a password hash, and a row somebody edited by hand "+
				"must not become a way in", stored)
		}
	}
}

func TestAnOlderCostIsStillReadable(t *testing.T) {
	stored, err := derive("correct horse battery staple", []byte("0123456789abcdef"), 1000)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}

	same, err := matches(stored, "correct horse battery staple")
	if err != nil || !same {
		t.Fatalf("same = %v err = %v: the cost is stored with the hash so it can be "+
			"raised later without locking everybody out", same, err)
	}
}
