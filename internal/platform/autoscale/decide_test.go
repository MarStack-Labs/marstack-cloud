package autoscale

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var noon = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func policyOf(min, max, target int) Policy {
	return Policy{Min: min, Max: max, TargetCPU: target}
}

func groupOf(replicas int, age time.Duration) Group {
	members := make([]Member, 0, replicas)
	for i := range replicas {
		members = append(members, Member{
			InstanceID: "i-" + strconv.Itoa(i),
			CreatedAt:  noon.Add(-age),
		})
	}
	return Group{ServiceID: "svc-1", Name: "web", Replicas: replicas,
		Settled: true, Members: members}
}

func loadOf(group Group, cpu float64, age time.Duration) map[string]Sample {
	samples := make(map[string]Sample, len(group.Members))
	for _, member := range group.Members {
		samples[member.InstanceID] = Sample{
			CPUPercent: cpu,
			ReportedAt: noon.Add(-age),
		}
	}
	return samples
}

func TestLoadOverTheTargetAddsReplicas(t *testing.T) {
	group := groupOf(2, 10*time.Minute)
	got := decide(policyOf(1, 10, 50), group, loadOf(group, 100, time.Second), noon)

	if !got.Act || got.Replicas != 4 {
		t.Fatalf("decision = %+v, want 4: twice the target across two replicas is four",
			got)
	}
}

func TestLoadUnderTheTargetTakesReplicasAway(t *testing.T) {
	group := groupOf(4, 10*time.Minute)
	got := decide(policyOf(1, 10, 80), group, loadOf(group, 20, time.Second), noon)

	if !got.Act || got.Replicas != 1 {
		t.Fatalf("decision = %+v, want 1", got)
	}
}

func TestLoadNearTheTargetChangesNothing(t *testing.T) {
	group := groupOf(3, 10*time.Minute)

	for _, cpu := range []float64{62, 70, 78} {
		got := decide(policyOf(1, 10, 70), group, loadOf(group, cpu, time.Second), noon)
		if got.Act {
			t.Fatalf("cpu %v moved the count to %d: without a deadband a service scales "+
				"back and forth over a rounding error forever", cpu, got.Replicas)
		}
	}
}

func TestTheBoundsAreRespected(t *testing.T) {
	high := groupOf(4, 10*time.Minute)
	if got := decide(policyOf(1, 5, 10), high, loadOf(high, 100, time.Second), noon); got.Replicas > 5 {
		t.Fatalf("replicas = %d, want at most the maximum of 5", got.Replicas)
	}

	low := groupOf(4, 10*time.Minute)
	got := decide(policyOf(3, 10, 90), low, loadOf(low, 1, time.Second), noon)
	if got.Act && got.Replicas < 3 {
		t.Fatalf("replicas = %d, want at least the minimum of 3", got.Replicas)
	}
}

func TestOneDecisionMovesAtMostAStep(t *testing.T) {
	group := groupOf(2, 10*time.Minute)
	got := decide(policyOf(1, 32, 5), group, loadOf(group, 100, time.Second), noon)

	if got.Replicas > 2+MaxStep {
		t.Fatalf("replicas = %d, want at most %d more than the %d it has: a single reading "+
			"must not be able to ask for a whole cluster", got.Replicas, MaxStep,
			group.Replicas)
	}
}

func TestStaleLoadStopsEverything(t *testing.T) {
	group := groupOf(2, 10*time.Minute)
	got := decide(policyOf(1, 10, 50), group, loadOf(group, 100, 5*time.Minute), noon)

	if got.Act {
		t.Fatal("it scaled on a reading five minutes old: scaling on a guess is worse " +
			"than not scaling, because the guess is what the load was before the last change")
	}
	if !strings.Contains(got.Reason, "stale") {
		t.Fatalf("reason = %q, want it to say why nothing happened", got.Reason)
	}
}

func TestAMissingSampleStopsEverything(t *testing.T) {
	group := groupOf(3, 10*time.Minute)
	samples := loadOf(group, 100, time.Second)
	delete(samples, "i-1")

	if got := decide(policyOf(1, 10, 50), group, samples, noon); got.Act {
		t.Fatal("a replica with no reading was left out of the average, so the answer is " +
			"about the replicas that happened to report rather than the service")
	}
}

func TestAWarmingReplicaIsNotCountedAgainstTheService(t *testing.T) {
	group := groupOf(2, 10*time.Minute)
	group.Members = append(group.Members, Member{InstanceID: "i-new", CreatedAt: noon})
	group.Replicas = 3

	samples := loadOf(group, 90, time.Second)
	samples["i-new"] = Sample{CPUPercent: 0, ReportedAt: noon}

	got := decide(policyOf(1, 10, 60), group, samples, noon)
	if !got.Act || got.Replicas <= 3 {
		t.Fatalf("decision = %+v, want more replicas: a replica that has just started "+
			"reads as idle, and averaging it in scales down the service that is busy", got)
	}
}

func TestEverythingWarmingUpMeansNoDecision(t *testing.T) {
	group := groupOf(2, time.Second)

	if got := decide(policyOf(1, 10, 50), group, loadOf(group, 0, time.Second), noon); got.Act {
		t.Fatal("it scaled off replicas that had not run long enough to have a load")
	}
}

func TestNothingHappensDuringTheCooldown(t *testing.T) {
	group := groupOf(2, 10*time.Minute)
	policy := policyOf(1, 10, 50)
	policy.LastAt = noon.Add(-time.Minute)

	got := decide(policy, group, loadOf(group, 100, time.Second), noon)
	if got.Act {
		t.Fatal("it scaled again a minute after the last change, before the replicas it " +
			"just made could take any load: that is how a service oscillates")
	}
}

func TestAnUnsettledServiceIsLeftAlone(t *testing.T) {
	group := groupOf(3, 10*time.Minute)
	group.Settled = false

	if got := decide(policyOf(1, 10, 50), group, loadOf(group, 100, time.Second), noon); got.Act {
		t.Fatal("it scaled a service that has not reached the count it already has, so it " +
			"is deciding from a number that is not true yet")
	}
}

func TestAtTheCeilingItSaysSoRatherThanNothing(t *testing.T) {
	group := groupOf(5, 10*time.Minute)
	got := decide(policyOf(1, 5, 10), group, loadOf(group, 100, time.Second), noon)

	if got.Act {
		t.Fatalf("decision = %+v, want no change at the maximum", got)
	}
	if !strings.Contains(got.Reason, "maximum") {
		t.Fatalf("reason = %q, want it to name the ceiling: an operator watching a service "+
			"drown needs to know the limit is theirs to raise", got.Reason)
	}
}
