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

func decide(policy Policy, group Group, samples map[string]Sample, now time.Time) Decision {
	if !group.Settled {
		return Decision{Reason: "the service has not reached its replica count yet"}
	}
	if !policy.LastAt.IsZero() && now.Sub(policy.LastAt) < Cooldown {
		return Decision{Reason: "waiting out the cooldown after the last change"}
	}

	ready := 0
	var total float64
	for _, member := range group.Members {
		if now.Sub(member.CreatedAt) < Warmup {
			continue
		}

		sample, held := samples[member.InstanceID]
		if !held || now.Sub(sample.ReportedAt) > StaleLoad {
			return Decision{Reason: "the load of " + member.InstanceID +
				" is missing or stale, and scaling on a guess is worse than not scaling"}
		}
		total += sample.CPUPercent
		ready++
	}

	if ready == 0 {
		return Decision{Reason: "every replica is still warming up"}
	}

	average := total / float64(ready)
	target := float64(policy.TargetCPU)

	if math.Abs(average-target) <= Deadband {
		return Decision{Reason: "the load is within " +
			strconv.FormatFloat(Deadband, 'f', 0, 64) + " points of the target"}
	}

	wanted := int(math.Ceil(float64(group.Replicas) * average / target))
	wanted = step(group.Replicas, wanted)
	wanted = clamp(wanted, policy.Min, policy.Max)

	if wanted == group.Replicas {
		return Decision{Reason: reasonFor(average, target, group.Replicas, policy)}
	}

	return Decision{
		Replicas: wanted,
		Act:      true,
		Reason: "average cpu " + round(average) + "% against a target of " +
			round(target) + "%",
	}
}

func reasonFor(average, target float64, replicas int, policy Policy) string {
	switch {
	case average > target && replicas >= policy.Max:
		return "over target but already at the maximum of " + strconv.Itoa(policy.Max)
	case average < target && replicas <= policy.Min:
		return "under target but already at the minimum of " + strconv.Itoa(policy.Min)
	default:
		return "the load does not move the replica count"
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
