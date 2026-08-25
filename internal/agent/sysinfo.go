package agent

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type hostInfo struct {
	Arch      string
	OS        string
	CPUs      int
	MemoryMiB int
}

func inspectHost() hostInfo {
	return hostInfo{
		Arch:      runtime.GOARCH,
		OS:        runtime.GOOS,
		CPUs:      runtime.NumCPU(),
		MemoryMiB: totalMemoryMiB(),
	}
}

func totalMemoryMiB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kib, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0
		}
		return kib / 1024
	}
	return 0
}
