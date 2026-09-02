//go:build linux

package container

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func killedError(t *testing.T) error {
	t.Helper()

	cmd := exec.Command("sh", "-c", "kill -KILL $$")
	err := cmd.Run()
	if err == nil {
		t.Fatal("a process that kills itself reported success")
	}
	return err
}

func TestRunningOutOfTimeBeatsTheSignalThatDidIt(t *testing.T) {
	code, message := outcome(killedError(t), context.DeadlineExceeded)

	if message == "" {
		t.Fatal("a command that ran out of time came back with no reason. Killing it makes " +
			"Run return an exit error, so a check for that first always matches and the " +
			"caller is told \"exited with -1\" instead of what happened")
	}
	if !strings.Contains(message, "time") {
		t.Fatalf("message = %q, want it to name the timeout", message)
	}
	if code != -1 {
		t.Fatalf("code = %d, want -1", code)
	}
}

func TestAnExitCodeSurvivesWhenNothingTimedOut(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 7")
	code, message := outcome(cmd.Run(), nil)

	if code != 7 {
		t.Fatalf("code = %d, want the 7 the command exited with", code)
	}
	if message != "" {
		t.Fatalf("message = %q, want nothing to explain", message)
	}
}

func TestASuccessIsZeroAndSilent(t *testing.T) {
	code, message := outcome(nil, nil)
	if code != 0 || message != "" {
		t.Fatalf("code = %d message = %q", code, message)
	}
}

func TestSomethingThatIsNotAnExitIsExplained(t *testing.T) {
	code, message := outcome(errors.New("nsenter: not found"), nil)
	if code != -1 || !strings.Contains(message, "nsenter") {
		t.Fatalf("code = %d message = %q, want the reason carried through", code, message)
	}
}

func TestTheEndOfTheOutputIsKept(t *testing.T) {
	out, truncated := tail([]byte("start-middle-END"), 3)
	if out != "END" || !truncated {
		t.Fatalf("out = %q truncated = %v, want the end, which is where a command says "+
			"what went wrong", out, truncated)
	}
}
