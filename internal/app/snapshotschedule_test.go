package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type scheduleBody struct {
	ID       string `json:"id"`
	VolumeID string `json:"volume_id"`
	Every    string `json:"every"`
	Keep     int    `json:"keep"`
}

type scheduleList struct {
	Schedules []scheduleBody `json:"schedules"`
}

func setSnapshotSchedule(t *testing.T, a *testApp, volume, body string) (int, scheduleBody) {
	t.Helper()

	rec := do(t, a, http.MethodPut, "/v1/volumes/"+volume+"/snapshot-schedule",
		strings.NewReader(body))

	var got scheduleBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got
}

func TestASnapshotScheduleIsRecordedAndListed(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, _ := attachedVolume(t, a, nodeID, "scheduled")

	code, got := setSnapshotSchedule(t, a, volume, `{"every":"1h","keep":3}`)
	if code != http.StatusOK {
		t.Fatalf("set: %d", code)
	}
	if got.Keep != 3 || got.Every != "1h0m0s" {
		t.Fatalf("schedule = %+v", got)
	}

	rec := do(t, a, http.MethodGet, "/v1/snapshot-schedules", nil)
	var list scheduleList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Schedules) != 1 || list.Schedules[0].VolumeID != volume {
		t.Fatalf("schedules = %+v", list.Schedules)
	}
}

func TestSettingAScheduleTwiceReplacesItRatherThanAddingOne(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, _ := attachedVolume(t, a, nodeID, "scheduled")

	_, first := setSnapshotSchedule(t, a, volume, `{"every":"1h","keep":3}`)
	_, second := setSnapshotSchedule(t, a, volume, `{"every":"2h","keep":5}`)

	if first.ID != second.ID {
		t.Fatalf("ids %s and %s differ, so a volume can collect schedules", first.ID, second.ID)
	}
	if second.Keep != 5 || second.Every != "2h0m0s" {
		t.Fatalf("schedule = %+v, want the new values", second)
	}

	rec := do(t, a, http.MethodGet, "/v1/snapshot-schedules", nil)
	var list scheduleList
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Schedules) != 1 {
		t.Fatalf("schedules = %d, want one per volume", len(list.Schedules))
	}
}

func TestABadScheduleIsRefused(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, _ := attachedVolume(t, a, nodeID, "scheduled")

	for _, body := range []string{
		`{"every":"1s","keep":3}`,
		`{"every":"nonsense","keep":3}`,
		`{"every":"1h","keep":0}`,
		`{"every":"1h","keep":9999}`,
	} {
		if code, _ := setSnapshotSchedule(t, a, volume, body); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", body, code, http.StatusBadRequest)
		}
	}
}

func TestClearingAScheduleStopsIt(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, _ := attachedVolume(t, a, nodeID, "scheduled")

	setSnapshotSchedule(t, a, volume, `{"every":"1h","keep":3}`)

	rec := do(t, a, http.MethodDelete, "/v1/volumes/"+volume+"/snapshot-schedule", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}

	list := do(t, a, http.MethodGet, "/v1/snapshot-schedules", nil)
	var body scheduleList
	_ = json.Unmarshal(list.Body.Bytes(), &body)
	if len(body.Schedules) != 0 {
		t.Fatalf("schedules = %+v, want none left", body.Schedules)
	}
}

func TestAScheduleOnSomebodyElsesVolumeIsNotFound(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, _ := attachedVolume(t, a, nodeID, "scheduled")

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, other, http.MethodPut, "/v1/volumes/"+volume+"/snapshot-schedule",
		strings.NewReader(`{"every":"1h","keep":3}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
