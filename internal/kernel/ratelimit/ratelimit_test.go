package ratelimit

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func frozen() (func() time.Time, func(time.Duration)) {
	at := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex

	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return at
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(d)
	}
	return now, advance
}

func TestABurstIsSpentThenRefused(t *testing.T) {
	now, _ := frozen()
	l := New(10, 3, now)

	for i := range 3 {
		if ok, _ := l.Allow("caller"); !ok {
			t.Fatalf("request %d refused inside the burst", i)
		}
	}
	ok, after := l.Allow("caller")
	if ok {
		t.Fatal("a fourth request was allowed with the burst spent")
	}
	if after <= 0 {
		t.Fatal("the refusal carries no retry hint, so a client can only guess")
	}
}

func TestWaitingRefills(t *testing.T) {
	now, advance := frozen()
	l := New(10, 3, now)

	for range 3 {
		l.Allow("caller")
	}
	advance(time.Second)

	for i := range 3 {
		if ok, _ := l.Allow("caller"); !ok {
			t.Fatalf("request %d refused a second later, so the bucket never refills", i)
		}
	}
}

func TestOneCallerCannotSpendAnother(t *testing.T) {
	now, _ := frozen()
	l := New(10, 2, now)

	for range 2 {
		l.Allow("loud")
	}
	if ok, _ := l.Allow("loud"); ok {
		t.Fatal("the loud caller was not limited")
	}
	if ok, _ := l.Allow("quiet"); !ok {
		t.Fatal("a quiet caller was refused because another one was loud")
	}
}

func TestTrackingIsCappedSoAKeyFloodIsNotTheDoS(t *testing.T) {
	now, _ := frozen()
	l := New(10, 10, now)

	for i := range MaxTracked * 2 {
		l.Allow("caller-" + strconv.Itoa(i))
	}

	if got := l.Tracked(); got > MaxTracked {
		t.Fatalf("tracked = %d, want at most %d: a limiter that allocates per attacker-chosen "+
			"key is the denial of service it exists to stop", got, MaxTracked)
	}
	if !l.Overflowed() {
		t.Fatal("overflow was never recorded, so nothing would ever say why callers share a bucket")
	}
}

func TestIdleCallersAreForgotten(t *testing.T) {
	now, advance := frozen()
	l := New(10, 10, now)

	l.Allow("one-off")
	if l.Tracked() != 1 {
		t.Fatalf("tracked = %d, want 1", l.Tracked())
	}

	advance(2 * idle)
	l.Allow("someone-else")

	if got := l.Tracked(); got != 1 {
		t.Fatalf("tracked = %d, want the idle caller swept and only the new one left", got)
	}
}

func TestZeroMeansNoLimit(t *testing.T) {
	l := New(0, 0, nil)

	for range 10_000 {
		if ok, _ := l.Allow("caller"); !ok {
			t.Fatal("a limiter built with 0 refused a request")
		}
	}
}
