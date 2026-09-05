package alert

import (
	"strings"
	"testing"
	"time"
)

func at(minutes int) time.Time {
	return time.Date(2026, 9, 6, 12, minutes, 0, 0, time.UTC)
}

func watching(state string, since time.Time) Alert {
	return Alert{
		Metric:     MetricCPU,
		Comparison: Above,
		Threshold:  80,
		For:        2 * time.Minute,
		State:      state,
		Since:      since,
	}
}

func busy(now time.Time, percent float64) Sample {
	return Sample{CPUPercent: percent, ReportedAt: now}
}

func TestOneSpikeIsNotAnAlert(t *testing.T) {
	held := watching(StateQuiet, at(0))

	out := decide(held, busy(at(1), 95), true, at(1))
	if out.State != StateWarming {
		t.Fatalf("state = %q, want warming: a reading that has been high for one pass is a "+
			"spike, and paging somebody for a spike is how alerts get muted", out.State)
	}

	held.State = StateWarming
	held.Since = at(1)
	if out := decide(held, busy(at(2), 95), true, at(2)); out.State != StateWarming {
		t.Fatalf("state = %q, want it still warming one minute into a two minute window",
			out.State)
	}
}

func TestItFiresOnceTheReadingHolds(t *testing.T) {
	held := watching(StateWarming, at(1))

	out := decide(held, busy(at(4), 91), true, at(4))
	if out.State != StateFiring {
		t.Fatalf("state = %q, want firing after the window", out.State)
	}
	if !out.Changed {
		t.Fatal("the change was not announced, so nothing downstream hears about it")
	}
	if out.Message == "" {
		t.Fatal("firing with no message leaves the receiver to guess what crossed what")
	}
}

func TestAFiringAlertDoesNotKeepAnnouncingItself(t *testing.T) {
	held := watching(StateFiring, at(1))

	out := decide(held, busy(at(9), 93), true, at(9))
	if out.State != StateFiring {
		t.Fatalf("state = %q, want it still firing", out.State)
	}
	if out.Changed {
		t.Fatal("a condition that has not changed was announced again. A sweep every twenty " +
			"seconds would then write an event every twenty seconds forever")
	}
}

func TestItClearsWhenTheReadingComesBack(t *testing.T) {
	held := watching(StateFiring, at(1))

	out := decide(held, busy(at(9), 12), true, at(9))
	if out.State != StateQuiet {
		t.Fatalf("state = %q, want quiet", out.State)
	}
	if !out.Changed {
		t.Fatal("the recovery was not announced, so whoever was told it fired is never told " +
			"it stopped")
	}
}

func TestAMissingReadingIsNotZero(t *testing.T) {
	held := watching(StateFiring, at(1))

	out := decide(held, Sample{}, false, at(9))
	if out.State != StateQuiet {
		t.Fatalf("state = %q, want the alert stopped rather than decided", out.State)
	}
	if !strings.Contains(out.Message, "no sample has arrived") {
		t.Fatalf("message = %q, want it to say no sample ever arrived. A workload that has "+
			"never reported and one that stopped reporting are different things to chase, "+
			"and telling an operator the reading is old when there was never a reading "+
			"sends them to the wrong place", out.Message)
	}

	below := watching(StateQuiet, at(1))
	below.Comparison = Below
	below.Threshold = 20
	if out := decide(below, Sample{}, false, at(9)); out.State == StateWarming {
		t.Fatal("a workload that stopped reporting was read as zero, which fires every " +
			"below-threshold alert the moment a node goes quiet")
	}
}

func TestAStaleReadingIsNotUsed(t *testing.T) {
	held := watching(StateQuiet, at(0))

	out := decide(held, busy(at(0), 99), true, at(30))
	if out.State != StateQuiet {
		t.Fatalf("state = %q, want quiet: a half hour old reading says nothing about now",
			out.State)
	}
	if out.Message == "" {
		t.Fatal("nothing said the reading was stale")
	}
}

func TestMemoryNeedsALimitToBeAPercentage(t *testing.T) {
	held := watching(StateQuiet, at(0))
	held.Metric = MetricMemory

	out := decide(held, Sample{MemoryPercent: 0, MemoryKnown: false, ReportedAt: at(1)},
		true, at(1))
	if out.State != StateQuiet {
		t.Fatalf("state = %q, want quiet: without a limit, a percentage of it is invented",
			out.State)
	}

	known := decide(held, Sample{MemoryPercent: 92, MemoryKnown: true, ReportedAt: at(1)},
		true, at(1))
	if known.State != StateWarming {
		t.Fatalf("state = %q, want warming once the limit is known", known.State)
	}

	low := watching(StateQuiet, at(0))
	low.Metric = MetricMemory
	low.Comparison = Below
	low.Threshold = 20

	if out := decide(low, Sample{MemoryKnown: false, ReportedAt: at(1)},
		true, at(1)); out.State != StateQuiet {
		t.Fatalf("state = %q, want quiet: with no limit the percentage reads zero, and zero "+
			"is below every below-threshold there is", out.State)
	}
}

func TestBelowFiresWhenTheReadingDrops(t *testing.T) {
	held := watching(StateWarming, at(1))
	held.Comparison = Below
	held.Threshold = 5

	if out := decide(held, busy(at(4), 1), true, at(4)); out.State != StateFiring {
		t.Fatalf("state = %q, want firing: a workload doing nothing is a real thing to "+
			"watch for", out.State)
	}
	if out := decide(held, busy(at(4), 40), true, at(4)); out.State != StateQuiet {
		t.Fatalf("state = %q, want quiet when the reading is above a below-threshold",
			out.State)
	}
}
