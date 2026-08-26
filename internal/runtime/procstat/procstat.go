package procstat

import (
	"os"
	"strconv"
	"strings"
)

const ticksPerSecond = 100

func Of(pid int) (float64, int, bool) {
	seconds, ok := cpuSeconds(pid)
	if !ok {
		return 0, 0, false
	}
	return seconds, residentMiB(pid), true
}

func cpuSeconds(pid int) (float64, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}

	text := string(raw)
	tail := strings.LastIndex(text, ")")
	if tail < 0 {
		return 0, false
	}

	fields := strings.Fields(text[tail+1:])
	if len(fields) < 13 {
		return 0, false
	}

	user, err := strconv.ParseFloat(fields[11], 64)
	if err != nil {
		return 0, false
	}
	system, err := strconv.ParseFloat(fields[12], 64)
	if err != nil {
		return 0, false
	}
	return (user + system) / ticksPerSecond, true
}

func residentMiB(pid int) int {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}

	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}
