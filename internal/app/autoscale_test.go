package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type scalerBody struct {
	ID         string `json:"id"`
	ServiceID  string `json:"service_id"`
	Min        int    `json:"min"`
	Max        int    `json:"max"`
	TargetCPU  int    `json:"target_cpu"`
	LastReason string `json:"last_reason"`
}

func setAutoscale(t *testing.T, a *testApp, serviceID, body string) (int, scalerBody) {
	t.Helper()

	rec := do(t, a, http.MethodPut, "/v1/services/"+serviceID+"/autoscale",
		strings.NewReader(body))

	var got scalerBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got
}

func readAutoscale(t *testing.T, a *testApp, serviceID string) (int, scalerBody) {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/services/"+serviceID+"/autoscale", nil)

	var got scalerBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got
}

func scaledService(t *testing.T, a *testApp) serviceBody {
	t.Helper()

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":2,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 1)
	return created
}

func TestAPolicyIsKeptAgainstItsService(t *testing.T) {
	a, _ := newBalancingApp(t)
	created := scaledService(t, a)

	code, policy := setAutoscale(t, a, created.ID, `{"min":1,"max":8,"target_cpu":70}`)
	if code != http.StatusOK {
		t.Fatalf("set: %d", code)
	}
	if policy.ServiceID != created.ID || policy.Max != 8 {
		t.Fatalf("policy = %+v", policy)
	}

	if code, held := readAutoscale(t, a, created.ID); code != http.StatusOK ||
		held.TargetCPU != 70 {
		t.Fatalf("read: %d %+v", code, held)
	}
}

func TestSettingAPolicyTwiceReplacesIt(t *testing.T) {
	a, _ := newBalancingApp(t)
	created := scaledService(t, a)

	setAutoscale(t, a, created.ID, `{"min":1,"max":8,"target_cpu":70}`)
	setAutoscale(t, a, created.ID, `{"min":2,"max":4,"target_cpu":40}`)

	_, held := readAutoscale(t, a, created.ID)
	if held.Min != 2 || held.Max != 4 || held.TargetCPU != 40 {
		t.Fatalf("policy = %+v, want the second one: a service that scales itself two "+
			"different ways has no answer", held)
	}

	rec := do(t, a, http.MethodGet, "/v1/autoscalers", nil)
	var list struct {
		Autoscalers []scalerBody `json:"autoscalers"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Autoscalers) != 1 {
		t.Fatalf("autoscalers = %d, want 1", len(list.Autoscalers))
	}
}

func TestAPolicyThatCannotHoldIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)
	created := scaledService(t, a)

	for _, body := range []string{
		`{"min":0,"max":4,"target_cpu":70}`,
		`{"min":5,"max":2,"target_cpu":70}`,
		`{"min":1,"max":4,"target_cpu":0}`,
		`{"min":1,"max":4,"target_cpu":140}`,
	} {
		if code, _ := setAutoscale(t, a, created.ID, body); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", body, code, http.StatusBadRequest)
		}
	}
}

func TestAPolicyNeedsAServiceThatExists(t *testing.T) {
	a, _ := newBalancingApp(t)

	if code, _ := setAutoscale(t, a, "svc-nothing",
		`{"min":1,"max":4,"target_cpu":70}`); code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", code, http.StatusNotFound)
	}
}

func TestAnotherProjectsPolicyIsNotFound(t *testing.T) {
	a, _ := newBalancingApp(t)
	created := scaledService(t, a)
	setAutoscale(t, a, created.ID, `{"min":1,"max":4,"target_cpu":70}`)

	outsider := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, outsider, http.MethodGet,
		"/v1/services/"+created.ID+"/autoscale", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestAPolicyCanBeTakenAway(t *testing.T) {
	a, _ := newBalancingApp(t)
	created := scaledService(t, a)
	setAutoscale(t, a, created.ID, `{"min":1,"max":4,"target_cpu":70}`)

	if rec := do(t, a, http.MethodDelete, "/v1/services/"+created.ID+"/autoscale",
		nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := readAutoscale(t, a, created.ID); code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", code, http.StatusNotFound)
	}
}

func TestASweepWithNoLoadSaysWhyRatherThanScaling(t *testing.T) {
	a, _ := newBalancingApp(t)
	created := scaledService(t, a)
	setAutoscale(t, a, created.ID, `{"min":1,"max":8,"target_cpu":70}`)

	a.scalers.Sweep(context.Background())

	_, held := readAutoscale(t, a, created.ID)
	if held.LastReason == "" {
		t.Fatal("the sweep did nothing and recorded nothing, so an operator watching a " +
			"service that will not scale has no way to find out why")
	}
	if readService(t, a, a.secret, created.ID).Replicas != 2 {
		t.Fatal("it scaled a service whose replicas have never reported a load")
	}
}

func TestAPolicyForAServiceThatIsGoneIsDropped(t *testing.T) {
	a, _ := newBalancingApp(t)
	created := scaledService(t, a)
	setAutoscale(t, a, created.ID, `{"min":1,"max":4,"target_cpu":70}`)

	if rec := do(t, a, http.MethodDelete, "/v1/services/"+created.ID, nil); rec.Code !=
		http.StatusNoContent {
		t.Fatalf("delete service: %d %s", rec.Code, rec.Body.String())
	}
	a.scalers.Sweep(context.Background())

	rec := do(t, a, http.MethodGet, "/v1/autoscalers", nil)
	var list struct {
		Autoscalers []scalerBody `json:"autoscalers"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)

	if len(list.Autoscalers) != 0 {
		t.Fatalf("autoscalers = %d, want none: a policy pointing at a service that no "+
			"longer exists is swept over on every pass forever", len(list.Autoscalers))
	}
}
