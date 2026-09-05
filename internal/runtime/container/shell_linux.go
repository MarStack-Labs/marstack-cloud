//go:build linux

package container

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func (r *Runtime) Shell(
	ctx context.Context, instanceID string, command []string,
	in io.Reader, out io.Writer,
) error {
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	pid, alive := r.livePID(instanceID)
	if !alive {
		return fmt.Errorf("container %s is not running here", instanceID)
	}

	args := append([]string{
		"--target", strconv.Itoa(pid),
		"--mount", "--uts", "--ipc", "--net", "--pid",
		"--",
	}, command...)

	cmd := exec.CommandContext(ctx, "nsenter", args...)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = killDelay

	r.log.Info("shell opened in a container", "instance", instanceID, "command", command)
	return cmd.Run()
}

var _ workload.Sheller = (*Runtime)(nil)
