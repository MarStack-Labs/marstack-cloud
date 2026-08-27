package interval

import (
	"testing"
	"time"
)

func TestADurationIsReadInDaysAndWeeksAsWellAsHours(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"":     0,
		"12h":  12 * time.Hour,
		"90m":  90 * time.Minute,
		"30d":  30 * 24 * time.Hour,
		"4w":   4 * 7 * 24 * time.Hour,
		" 6h ": 6 * time.Hour,
	} {
		got, err := Parse(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if got != want {
			t.Fatalf("%q = %v, want %v", text, got, want)
		}
	}
}

func TestWhatIsNotADurationIsRefused(t *testing.T) {
	for _, text := range []string{"soon", "30 days", "1y", "d", "w", "-", "12hh"} {
		if _, err := Parse(text); err == nil {
			t.Fatalf("%q was accepted", text)
		}
	}
}

func TestANegativeDurationIsReadAsNegative(t *testing.T) {
	got, err := Parse("-1h")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != -time.Hour {
		t.Fatalf("got %v, want the caller to see the sign and decide", got)
	}
}
