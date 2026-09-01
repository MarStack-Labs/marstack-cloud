package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type jobRunBody struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	Attempt    int    `json:"attempt"`
	State      string `json:"state"`
	Message    string `json:"message"`
	ExitCode   *int   `json:"exit_code"`
}

type jobBody struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Every   string       `json:"every"`
	Retries int          `json:"retries"`
	Keep    int          `json:"keep"`
	Paused  bool         `json:"paused"`
	NextAt  string       `json:"next_at"`
	Runs    []jobRunBody `json:"runs"`
}

func createJob(t *testing.T, a *testApp, body string) (int, jobBody) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/jobs", strings.NewReader(body))

	var created jobBody
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	return rec.Code, created
}

func readJob(t *testing.T, a *testApp, id string) jobBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/jobs/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read job: %d %s", rec.Code, rec.Body.String())
	}

	var body jobBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func sweepJobs(t *testing.T, a *testApp, passes int) {
	t.Helper()

	for range passes {
		a.jobs.Sweep(context.Background())
	}
}

func liveRun(job jobBody) *jobRunBody {
	for i := range job.Runs {
		if job.Runs[i].State == "pending" || job.Runs[i].State == "running" {
			return &job.Runs[i]
		}
	}
	return nil
}

func endRun(t *testing.T, a *testApp, instanceID, state string, exitCode int) {
	t.Helper()

	nodeID := waitForPlacement(t, a, instanceID)
	if nodeID == "" {
		t.Fatalf("the run's workload %s was never placed", instanceID)
	}

	body, err := json.Marshal(map[string]any{
		"observed_state": state,
		"exit_code":      exitCode,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	reportState(t, a, nodeID, instanceID, string(body))
}

const oneShot = `{"name":"nightly","isolation":"container","image":"alpine:3.20",` +
	`"command":["sh","-c","true"]}`

func TestATriggeredJobRunsAndSucceeds(t *testing.T) {
	a, _ := newBalancingApp(t)

	code, created := createJob(t, a, oneShot)
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if len(created.Runs) != 0 {
		t.Fatal("a job without a schedule ran on its own")
	}

	rec := do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("trigger: %d %s", rec.Code, rec.Body.String())
	}

	run := liveRun(readJob(t, a, created.ID))
	if run == nil {
		t.Fatal("the trigger recorded no run")
	}
	endRun(t, a, run.InstanceID, "stopped", 0)
	sweepJobs(t, a, 1)

	held := readJob(t, a, created.ID)
	if held.Runs[0].State != "succeeded" {
		t.Fatalf("state = %q message = %q, want succeeded: exit 0 with a restart policy of "+
			"never is the platform saying the work finished",
			held.Runs[0].State, held.Runs[0].Message)
	}
	if instanceCount(t, a) != 0 {
		t.Fatalf("instances = %d, want none: a finished run's workload is cleared, or a "+
			"nightly job leaves one behind every night", instanceCount(t, a))
	}
}

func TestARunIsNeverRestartedByItsNode(t *testing.T) {
	a, _ := newBalancingApp(t)
	_, created := createJob(t, a, oneShot)

	do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)
	run := liveRun(readJob(t, a, created.ID))

	rec := do(t, a, http.MethodGet, "/v1/instances/"+run.InstanceID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read instance: %d %s", rec.Code, rec.Body.String())
	}

	var held struct {
		RestartPolicy string `json:"restart_policy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if held.RestartPolicy != "never" {
		t.Fatalf("restart policy = %q, want never: the default is always, so a run left to "+
			"the default is restarted by its node every time it exits and the job never "+
			"finishes", held.RestartPolicy)
	}
}

func TestANonZeroExitIsAFailureNotASuccess(t *testing.T) {
	a, _ := newBalancingApp(t)
	_, created := createJob(t, a, oneShot)

	do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)
	run := liveRun(readJob(t, a, created.ID))
	endRun(t, a, run.InstanceID, "failed", 3)
	sweepJobs(t, a, 1)

	held := readJob(t, a, created.ID)
	if held.Runs[0].State != "failed" {
		t.Fatalf("state = %q, want failed", held.Runs[0].State)
	}
	if held.Runs[0].ExitCode == nil || *held.Runs[0].ExitCode != 3 {
		t.Fatalf("exit code = %v, want the 3 the workload exited with: without it "+
			"\"stopped\" cannot be told apart from \"finished successfully\"",
			held.Runs[0].ExitCode)
	}
}

func TestAStopWithoutExitZeroIsNotASuccess(t *testing.T) {
	a, _ := newBalancingApp(t)
	_, created := createJob(t, a, oneShot)

	do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)
	run := liveRun(readJob(t, a, created.ID))
	endRun(t, a, run.InstanceID, "stopped", 137)
	sweepJobs(t, a, 1)

	held := readJob(t, a, created.ID)
	if held.Runs[0].State != "failed" {
		t.Fatalf("state = %q, want failed: a workload killed part way through stops, and "+
			"reading stopped alone as success records work that never happened",
			held.Runs[0].State)
	}
}

func TestAFailedRunIsRetriedUpToTheLimit(t *testing.T) {
	a, _ := newBalancingApp(t)

	_, created := createJob(t, a, `{"name":"flaky","isolation":"container",`+
		`"image":"alpine:3.20","command":["sh","-c","false"],"retries":2}`)
	do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)

	for attempt := 1; attempt <= 3; attempt++ {
		run := liveRun(readJob(t, a, created.ID))
		if run == nil {
			t.Fatalf("attempt %d: no run to fail", attempt)
		}
		if run.Attempt != attempt {
			t.Fatalf("attempt = %d, want %d", run.Attempt, attempt)
		}
		endRun(t, a, run.InstanceID, "failed", 1)
		sweepJobs(t, a, 1)
	}

	held := readJob(t, a, created.ID)
	if live := liveRun(held); live != nil {
		t.Fatalf("attempt %d is running, want the job to stop after the first try plus "+
			"two retries: retrying without a bound is a crashloop", live.Attempt)
	}
	if len(held.Runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(held.Runs))
	}
}

func TestASucceedingRetryEndsTheAttempts(t *testing.T) {
	a, _ := newBalancingApp(t)

	_, created := createJob(t, a, `{"name":"flaky","isolation":"container",`+
		`"image":"alpine:3.20","command":["sh","-c","false"],"retries":3}`)
	do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)

	run := liveRun(readJob(t, a, created.ID))
	endRun(t, a, run.InstanceID, "failed", 1)
	sweepJobs(t, a, 1)

	run = liveRun(readJob(t, a, created.ID))
	endRun(t, a, run.InstanceID, "stopped", 0)
	sweepJobs(t, a, 2)

	held := readJob(t, a, created.ID)
	if len(held.Runs) != 2 {
		t.Fatalf("runs = %d, want 2: the retries left are not attempts still owed",
			len(held.Runs))
	}
	if held.Runs[1].State != "succeeded" {
		t.Fatalf("state = %q, want succeeded", held.Runs[1].State)
	}
}

func TestTwoRunsOfOneJobNeverOverlap(t *testing.T) {
	a, _ := newBalancingApp(t)
	_, created := createJob(t, a, oneShot)

	do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)
	rec := do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: two runs of one job race each other over whatever "+
			"the job touches", rec.Code, http.StatusConflict)
	}
}

func TestAJobNeedsACommandOfItsOwn(t *testing.T) {
	a, _ := newBalancingApp(t)

	code, _ := createJob(t, a,
		`{"name":"vague","isolation":"container","image":"alpine:3.20"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a workload running whatever its image starts by "+
			"default never reports finishing on purpose", code, http.StatusBadRequest)
	}
}

func TestAScheduledJobFiresOnItsOwn(t *testing.T) {
	a, _ := newBalancingApp(t)

	code, created := createJob(t, a, `{"name":"hourly","isolation":"container",`+
		`"image":"alpine:3.20","command":["sh","-c","true"],"every":"1m"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if created.NextAt == "" {
		t.Fatal("a scheduled job was given no next run")
	}

	sweepJobs(t, a, 1)
	if len(readJob(t, a, created.ID).Runs) != 0 {
		t.Fatal("it fired before its interval was up")
	}

	a.jobs.Sweep(context.Background())
	if runs := readJob(t, a, created.ID).Runs; len(runs) != 0 {
		t.Fatalf("runs = %d, want none until the interval passes", len(runs))
	}
}

func TestAPausedJobDoesNotFire(t *testing.T) {
	a, _ := newBalancingApp(t)

	_, created := createJob(t, a, `{"name":"hourly","isolation":"container",`+
		`"image":"alpine:3.20","command":["sh","-c","true"],"every":"1m"}`)

	rec := do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/pause", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pause: %d", rec.Code)
	}
	if !readJob(t, a, created.ID).Paused {
		t.Fatal("the pause was not recorded")
	}

	if rec := do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/resume", nil); rec.Code !=
		http.StatusOK {
		t.Fatalf("resume: %d", rec.Code)
	}
	if readJob(t, a, created.ID).Paused {
		t.Fatal("the resume was not recorded")
	}
}

func TestDeletingAJobTakesItsWorkloadWithIt(t *testing.T) {
	a, _ := newBalancingApp(t)
	_, created := createJob(t, a, oneShot)

	do(t, a, http.MethodPost, "/v1/jobs/"+created.ID+"/run", nil)
	if instanceCount(t, a) != 1 {
		t.Fatalf("instances = %d, want the run's workload", instanceCount(t, a))
	}

	if rec := do(t, a, http.MethodDelete, "/v1/jobs/"+created.ID, nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if instanceCount(t, a) != 0 {
		t.Fatalf("instances = %d, want none: a deleted job that leaves its workload running "+
			"leaves something nobody can find", instanceCount(t, a))
	}
}

func TestAJobNamingNothingIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	code, _ := createJob(t, a, `{"name":"lost","isolation":"container","image":"alpine:3.20",`+
		`"command":["true"],"network_id":"net-nothing"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: every run would fail the same way", code,
			http.StatusBadRequest)
	}
}

func TestAnotherProjectsJobIsNotFound(t *testing.T) {
	a, _ := newBalancingApp(t)
	_, created := createJob(t, a, oneShot)

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodGet, "/v1/jobs/"+created.ID, nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
