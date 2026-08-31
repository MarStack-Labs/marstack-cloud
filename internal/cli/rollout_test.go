package cli

import (
	"strings"
	"testing"
)

func TestASealedRevisionCannotBePublishedByAccident(t *testing.T) {
	current := serviceView{
		Name:      "web",
		Revision:  4,
		EnvNames:  []string{"DATABASE_URL", "TOKEN"},
		FilePaths: []string{"/etc/app.conf"},
	}

	err := checkCarried(current, 0, 0, false)
	if err == nil {
		t.Fatal("publishing would have dropped the environment and the files without " +
			"saying anything, and the replicas would come back up without them")
	}
	for _, want := range []string{"DATABASE_URL", "/etc/app.conf", "--drop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestPassingTheEnvAgainIsEnough(t *testing.T) {
	current := serviceView{Name: "web", Revision: 4, EnvNames: []string{"TOKEN"}}

	if err := checkCarried(current, 1, 0, false); err != nil {
		t.Fatalf("the environment was passed again and it still refused: %v", err)
	}
}

func TestDroppingIsAllowedWhenSaidOutLoud(t *testing.T) {
	current := serviceView{
		Name:      "web",
		Revision:  4,
		EnvNames:  []string{"TOKEN"},
		FilePaths: []string{"/etc/app.conf"},
	}

	if err := checkCarried(current, 0, 0, true); err != nil {
		t.Fatalf("--drop is the way to publish without them and it was refused: %v", err)
	}
}

func TestARevisionThatNeverCarriedAnythingNeedsNoFlag(t *testing.T) {
	if err := checkCarried(serviceView{Name: "web", Revision: 1}, 0, 0, false); err != nil {
		t.Fatalf("there is nothing to lose and it refused anyway: %v", err)
	}
}

func TestARolloutInProgressIsVisibleInTheRow(t *testing.T) {
	row := serviceRow(serviceView{
		Name:     "web",
		ID:       "svc-1",
		Replicas: 3,
		Revision: 2,
		Rollout:  serviceRolloutView{Current: 1, Stale: 2},
		Members:  []serviceMemberView{{}, {}, {}},
	})

	state := row[len(row)-1]
	if !strings.Contains(state, "rolling out") || !strings.Contains(state, "1/3") {
		t.Fatalf("state = %q, want the progress rather than a full count, because 3/3 up "+
			"while two replicas still run the old image reads as finished", state)
	}
}

func TestABlockedRolloutSaysSoInsteadOfShowingProgress(t *testing.T) {
	row := serviceRow(serviceView{
		Name:     "web",
		Replicas: 3,
		Revision: 2,
		Blocked:  "unknown_firewall: no firewall with that id exists",
		Rollout:  serviceRolloutView{Current: 0, Stale: 2},
		Members:  []serviceMemberView{{}, {}},
	})

	state := row[len(row)-1]
	if !strings.HasPrefix(state, "stuck:") {
		t.Fatalf("state = %q, want the reason: a rollout that cannot proceed looks the same "+
			"as one still working through the replicas", state)
	}
}
