package guest

import (
	"errors"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func TestWhatWasAskedForBeatsWhatTheImageDeclares(t *testing.T) {
	held, err := CommandFor([]string{"/bin/sh", "-c", "mine"}, []string{"/entrypoint"})
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if strings.Join(held, " ") != "/bin/sh -c mine" {
		t.Fatalf("command = %v, want what the caller asked for", held)
	}
}

func TestTheImageAnswersWhenNothingWasAsked(t *testing.T) {
	held, err := CommandFor(nil, []string{"/entrypoint"})
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if strings.Join(held, " ") != "/entrypoint" {
		t.Fatalf("command = %v, want the image's own", held)
	}
}

func TestNoCommandAnywhereCanNeverStart(t *testing.T) {
	_, err := CommandFor(nil, nil)
	if err == nil {
		t.Fatal("a workload with nothing to run was accepted")
	}
	if !errors.Is(err, workload.ErrUnstartable) {
		t.Fatalf("error = %v, want it marked unstartable: without that mark the node "+
			"retries it every pass for as long as it runs, and no amount of waiting adds "+
			"a command to an image", err)
	}
}
