package page

import (
	"net/http/httptest"
	"testing"
)

func windowFrom(t *testing.T, query string) (Window, error) {
	t.Helper()
	return From(httptest.NewRequest("GET", "/v1/things"+query, nil), 100, 500)
}

func TestAWindowDefaultsAndIsCapped(t *testing.T) {
	window, err := windowFrom(t, "")
	if err != nil {
		t.Fatalf("no query: %v", err)
	}
	if window.Limit != 100 {
		t.Fatalf("limit = %d, want the default", window.Limit)
	}
	if !window.After.Empty() {
		t.Fatal("a first page came back with a cursor")
	}

	if window, _ = windowFrom(t, "?limit=5"); window.Limit != 5 {
		t.Fatalf("limit = %d, want 5", window.Limit)
	}
	if window, _ = windowFrom(t, "?limit=9999"); window.Limit != 500 {
		t.Fatalf("limit = %d, want it capped at the ceiling", window.Limit)
	}
}

func TestANonsenseLimitIsRefusedRatherThanIgnored(t *testing.T) {
	for _, query := range []string{"?limit=0", "?limit=-3", "?limit=lots"} {
		if _, err := windowFrom(t, query); err == nil {
			t.Errorf("%q was accepted, so a typo would silently mean the default", query)
		}
	}
}

func TestACursorSurvivesARoundTrip(t *testing.T) {
	raw := Encode("2026-08-29T10:00:00Z", "i-abc")

	window, err := windowFrom(t, "?after="+raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if window.After.Order != "2026-08-29T10:00:00Z" || window.After.ID != "i-abc" {
		t.Fatalf("cursor = %+v, want both halves back", window.After)
	}
}

func TestACursorIsOpaqueAndCheckedRatherThanTrusted(t *testing.T) {
	if Encode("", "") != "" {
		t.Fatal("an empty cursor should encode to nothing")
	}

	raw := Encode("a", "b")
	if raw == "a\x00b" {
		t.Fatal("the cursor is not encoded, so a caller would read it as a promise")
	}

	for _, bad := range []string{"not-base64!!", "YWJj"} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("%q was accepted as a cursor", bad)
		}
	}
}
