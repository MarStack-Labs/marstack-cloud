//go:build linux

package microvm

import (
	"github.com/marstack-labs/marstack-cloud/internal/runtime/procstat"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (r *Runtime) Sample(instanceID string) (workload.Sample, bool) {
	pid, alive := r.livePID(instanceID)
	if !alive {
		return workload.Sample{}, false
	}

	seconds, memory, ok := procstat.Of(pid)
	if !ok {
		return workload.Sample{}, false
	}
	return workload.Sample{CPUSeconds: seconds, MemoryMiB: memory}, true
}
