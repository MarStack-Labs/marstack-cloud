//go:build linux

package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const killDelay = 2 * time.Second

func (r *Runtime) Enter(ctx context.Context, instanceID string, command []string,
	limit int) (workload.Entered, error) {
	if len(command) == 0 {
		return workload.Entered{}, errors.New("no command was given")
	}

	pid, alive := r.livePID(instanceID)
	if !alive {
		return workload.Entered{}, fmt.Errorf("container %s is not running here", instanceID)
	}

	args := append([]string{
		"--target", strconv.Itoa(pid),
		"--mount", "--uts", "--ipc", "--net", "--pid",
		"--",
	}, command...)

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "nsenter", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = killDelay

	err := cmd.Run()
	output, truncated := tail(out.Bytes(), limit)

	entered := workload.Entered{Output: output, Truncated: truncated}
	entered.ExitCode, entered.Message = outcome(err, ctx.Err())
	return entered, nil
}

func outcome(runErr, ctxErr error) (int, string) {
	if ctxErr != nil {
		return -1, "the command ran out of time"
	}
	if runErr == nil {
		return 0, ""
	}

	var exited *exec.ExitError
	if errors.As(runErr, &exited) {
		return exited.ExitCode(), ""
	}
	return -1, strings.TrimSpace(runErr.Error())
}

func tail(raw []byte, limit int) (string, bool) {
	if limit <= 0 || len(raw) <= limit {
		return string(raw), false
	}
	return string(raw[len(raw)-limit:]), true
}
