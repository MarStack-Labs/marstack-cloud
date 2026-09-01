package autoscale

import (
	"math"
	"strconv"
	"time"
)

type Decision struct {
	Replicas int
	Reason   string
	Act      bool
}

type pressure struct {
	name    string
	average float64
	target  float64
}

func (p pressure) demand() float64 {
	return p.average / p.target
}

func (p pressure) String() string {
	return "average " + p.name + " " + round(p.average) + "% against a target of " +
		round(p.target) + "%"
}

func decide(policy Policy, group Group, samples map[string]Sample, now time.Time) Decision {
	if !group.Settled {
		return Decision{Reason: "the service has not reached its replica count yet"}
	}
	if !policy.LastAt.IsZero() && now.Sub(policy.LastAt) < Cooldown {
		return Decision{Reason: "waiting out the cooldown after the last change"}
	}

	ready := 0
	var cpu, memory float64
	for _, member := range group.Members {
		if now.Sub(member.CreatedAt) < Warmup {
			continue
		}

		sample, held := samples[member.InstanceID]
		if !held || now.Sub(sample.ReportedAt) > StaleLoad {
			return Decision{Reason: "the load of " + member.InstanceID +
				" is missing or stale, and scaling on a guess is worse than not scaling"}
		}
		if policy.TargetMemory > 0 && !sample.MemoryKnown {
			return Decision{Reason: "how much memory " + member.InstanceID +
				" was given is not known, so a share of it cannot be worked out"}
		}
		cpu += sample.CPUPercent
		memory += sample.MemoryPercent
		ready++
	}

	if ready == 0 {
		return Decision{Reason: "every replica is still warming up"}
	}

	worst, ok := hardestPressed(policy, cpu/float64(ready), memory/float64(ready))
	if !ok {
		return Decision{Reason: "this policy names no target to scale against"}
	}

	if math.Abs(worst.demand()-1) <= DeadbandRatio {
		return Decision{Reason: worst.String() + ", which is close enough to leave alone"}
	}

	wanted := int(math.Ceil(float64(group.Replicas) * worst.demand()))
	wanted = step(group.Replicas, wanted)
	wanted = clamp(wanted, policy.Min, policy.Max)

	if wanted == group.Replicas {
		return Decision{Reason: reasonFor(worst, group.Replicas, policy)}
	}
	return Decision{Replicas: wanted, Act: true, Reason: worst.String()}
}

func hardestPressed(policy Policy, cpu, memory float64) (pressure, bool) {
	candidates := make([]pressure, 0, 2)
	if policy.TargetCPU > 0 {
		candidates = append(candidates, pressure{"cpu", cpu, float64(policy.TargetCPU)})
	}
	if policy.TargetMemory > 0 {
		candidates = append(candidates,
			pressure{"memory", memory, float64(policy.TargetMemory)})
	}
	if len(candidates) == 0 {
		return pressure{}, false
	}

	worst := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.demand() > worst.demand() {
			worst = candidate
		}
	}
	return worst, true
}

func reasonFor(worst pressure, replicas int, policy Policy) string {
	switch {
	case worst.demand() > 1 && replicas >= policy.Max:
		return worst.String() + ", but already at the maximum of " +
			strconv.Itoa(policy.Max)
	case worst.demand() < 1 && replicas <= policy.Min:
		return worst.String() + ", but already at the minimum of " +
			strconv.Itoa(policy.Min)
	default:
		return worst.String() + ", which does not move the replica count"
	}
}

func step(from, to int) int {
	if to > from+MaxStep {
		return from + MaxStep
	}
	if to < from-MaxStep {
		return from - MaxStep
	}
	return to
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func round(value float64) string {
	return strconv.FormatFloat(value, 'f', 1, 64)
}
