package agent

import (
	"context"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

type sampleMark struct {
	seconds float64
	at      time.Time
}

func (a *Agent) reportUsage(ctx context.Context, assigned []instanceView) {
	node, ok := a.sampleNode()
	if !ok {
		return
	}

	samples := make([]instanceUsageBody, 0, len(assigned))
	for _, in := range assigned {
		runtime, known := a.runtimeFor(in.Isolation)
		if !known {
			continue
		}

		sampler, able := runtime.(workload.Sampler)
		if !able {
			continue
		}

		sample, taken := sampler.Sample(in.ID)
		if !taken {
			continue
		}

		samples = append(samples, instanceUsageBody{
			InstanceID:    in.ID,
			CPUPercent:    a.cpuPercent(in.ID, sample.CPUSeconds),
			MemoryUsedMiB: sample.MemoryMiB,
		})
	}

	body := usageBody{
		CPUPercent:    node.CPUPercent,
		MemoryUsedMiB: node.MemoryUsedMiB,
		MemoryMiB:     node.MemoryMiB,
		Instances:     samples,
	}

	if err := a.client.reportUsage(ctx, a.currentNodeID(), body); err != nil {
		a.log.Warn("could not report usage", "error", err)
	}
}

func (a *Agent) cpuPercent(key string, seconds float64) float64 {
	now := a.now()

	a.marksMu.Lock()
	defer a.marksMu.Unlock()

	previous, seen := a.marks[key]
	a.marks[key] = sampleMark{seconds: seconds, at: now}

	if !seen {
		return 0
	}

	elapsed := now.Sub(previous.at).Seconds()
	if elapsed <= 0 || seconds < previous.seconds {
		return 0
	}
	return (seconds - previous.seconds) / elapsed * 100
}

func (a *Agent) forgetMarks(assigned []instanceView) {
	wanted := make(map[string]bool, len(assigned)+1)
	wanted[nodeMark] = true
	for _, in := range assigned {
		wanted[in.ID] = true
	}

	a.marksMu.Lock()
	defer a.marksMu.Unlock()

	for key := range a.marks {
		if !wanted[key] {
			delete(a.marks, key)
		}
	}
}
