//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
)

const (
	nodeMark = "\x00node"

	idleField   = 3
	iowaitField = 4
	clockTicks  = 100
)

type nodeUsage struct {
	CPUPercent    float64
	MemoryUsedMiB int
	MemoryMiB     int
}

func (a *Agent) sampleNode() (nodeUsage, bool) {
	busy, ok := busySeconds()
	if !ok {
		return nodeUsage{}, false
	}

	total, available, ok := memoryMiB()
	if !ok {
		return nodeUsage{}, false
	}

	return nodeUsage{
		CPUPercent:    a.cpuPercent(nodeMark, busy) / float64(max(a.host.CPUs, 1)),
		MemoryUsedMiB: total - available,
		MemoryMiB:     total,
	}, true
}

func busySeconds() (float64, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}

	line, _, _ := strings.Cut(string(raw), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, false
	}

	var busy float64
	for index, field := range fields[1:] {
		if index == idleField || index == iowaitField {
			continue
		}
		value, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return 0, false
		}
		busy += value
	}

	return busy / clockTicks, true
}

func memoryMiB() (int, int, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}

	total, available := 0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		switch fields[0] {
		case "MemTotal:":
			total = value / 1024
		case "MemAvailable:":
			available = value / 1024
		}
	}

	if total == 0 {
		return 0, 0, false
	}
	return total, available, true
}
