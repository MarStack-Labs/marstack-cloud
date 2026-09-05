package alert

import (
	"fmt"
	"time"
)

type verdict struct {
	State   string
	Value   float64
	Message string
	Changed bool
}

func decide(held Alert, sample Sample, found bool, now time.Time) verdict {
	if !found {
		return quieten(held, "no sample has arrived for this workload yet")
	}
	if now.Sub(sample.ReportedAt) > Stale {
		return quieten(held, fmt.Sprintf(
			"the last sample is %s old, and a reading nobody refreshed says nothing about now",
			now.Sub(sample.ReportedAt).Round(time.Second)))
	}

	value, known := reading(held.Metric, sample)
	if !known {
		return quieten(held,
			"this workload was given no memory limit, so a percentage of it is a guess")
	}

	if !crossed(held.Comparison, value, held.Threshold) {
		out := quieten(held, "")
		out.Value = value
		if held.State == StateFiring {
			out.Message = fmt.Sprintf("%s is %.1f%%, back %s %.1f%%",
				held.Metric, value, opposite(held.Comparison), held.Threshold)
		}
		return out
	}

	if held.State == StateQuiet {
		return verdict{State: StateWarming, Value: value, Changed: true,
			Message: fmt.Sprintf("%s is %.1f%%, waiting for it to hold for %s",
				held.Metric, value, held.For)}
	}
	if held.State == StateWarming {
		if now.Sub(held.Since) < held.For {
			return verdict{State: StateWarming, Value: value}
		}
		return verdict{State: StateFiring, Value: value, Changed: true,
			Message: fmt.Sprintf("%s has been %s %.1f%% for %s, and is %.1f%% now",
				held.Metric, held.Comparison, held.Threshold, held.For, value)}
	}
	return verdict{State: StateFiring, Value: value}
}

func quieten(held Alert, why string) verdict {
	out := verdict{State: StateQuiet, Message: why}
	out.Changed = held.State != StateQuiet
	return out
}

func reading(metric string, sample Sample) (float64, bool) {
	if metric == MetricMemory {
		return sample.MemoryPercent, sample.MemoryKnown
	}
	return sample.CPUPercent, true
}

func crossed(comparison string, value, threshold float64) bool {
	if comparison == Below {
		return value < threshold
	}
	return value > threshold
}

func opposite(comparison string) string {
	if comparison == Below {
		return Above
	}
	return Below
}
