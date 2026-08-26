package architecture_test

import (
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/agent"
	"github.com/marstack-labs/marstack-cloud/internal/platform/scheduler"
)

const fenceMargin = 30 * time.Second

func TestANodeFencesItselfBeforeTheSchedulerMovesItsWork(t *testing.T) {
	if agent.DefaultFenceAfter >= scheduler.DefaultStrandedGrace {
		t.Fatalf("fence after %s but the scheduler re-places after %s: a partitioned node would "+
			"still be running work the control plane has already given to somebody else",
			agent.DefaultFenceAfter, scheduler.DefaultStrandedGrace)
	}

	if scheduler.DefaultStrandedGrace-agent.DefaultFenceAfter < fenceMargin {
		t.Fatalf("only %s between fencing and re-placement: that is not enough for a slow stop "+
			"to finish, and the margin is the whole safety of this design",
			scheduler.DefaultStrandedGrace-agent.DefaultFenceAfter)
	}
}
