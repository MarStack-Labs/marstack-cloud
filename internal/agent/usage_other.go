//go:build !linux

package agent

const nodeMark = "\x00node"

type nodeUsage struct {
	CPUPercent    float64
	MemoryUsedMiB int
	MemoryMiB     int
}

func (a *Agent) sampleNode() (nodeUsage, bool) {
	return nodeUsage{}, false
}
