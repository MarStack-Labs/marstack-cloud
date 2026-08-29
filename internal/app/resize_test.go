package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type sizeBody struct {
	ID        string `json:"id"`
	VCPU      int    `json:"vcpu"`
	MemoryMiB int    `json:"memory_mib"`
}

func resize(
	t *testing.T, a *testApp, id, body string,
) (*httptest.ResponseRecorder, sizeBody) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/instances/"+id+"/resize", strings.NewReader(body))

	var got sizeBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec, got
}

func sizeOf(t *testing.T, a *testApp, id string) sizeBody {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/instances/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}

	var got sizeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestResizingChangesBothDimensions(t *testing.T) {
	a, _ := newBalancingApp(t)
	id := newInstance(t, a, a.secret, "web")

	rec, got := resize(t, a, id, `{"vcpu":2,"memory_mib":1024}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("resize: %d %s", rec.Code, rec.Body.String())
	}
	if got.VCPU != 2 || got.MemoryMiB != 1024 {
		t.Fatalf("response = %+v, want the new size", got)
	}

	if stored := sizeOf(t, a, id); stored.VCPU != 2 || stored.MemoryMiB != 1024 {
		t.Fatalf("stored = %+v, want the resize to have been written", stored)
	}
}

func TestAnOmittedDimensionIsLeftAlone(t *testing.T) {
	a, _ := newBalancingApp(t)
	id := newInstance(t, a, a.secret, "web")
	before := sizeOf(t, a, id)

	_, got := resize(t, a, id, `{"memory_mib":1024}`)
	if got.MemoryMiB != 1024 {
		t.Fatalf("memory = %d, want the new value", got.MemoryMiB)
	}
	if got.VCPU != before.VCPU {
		t.Fatalf("vcpu = %d, want it untouched at %d", got.VCPU, before.VCPU)
	}
}

func TestASizeOutsideTheLimitsIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)
	id := newInstance(t, a, a.secret, "web")

	for _, body := range []string{
		`{"vcpu":-1}`,
		`{"vcpu":100000}`,
		`{"memory_mib":1}`,
		`{"memory_mib":100000000}`,
	} {
		rec, _ := resize(t, a, id, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestGrowingPastTheQuotaIsRefusedAndShrinkingIsNot(t *testing.T) {
	a, _ := newBalancingApp(t)
	id := newInstance(t, a, a.secret, "web")

	before := sizeOf(t, a, id)
	setQuota(t, a, "prj-default", `{"vcpu":`+strconv.Itoa(before.VCPU)+`}`)

	rec, _ := resize(t, a, id, `{"vcpu":`+strconv.Itoa(before.VCPU+4)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: growing past the quota must be refused the same "+
			"way creating past it is", rec.Code, http.StatusConflict)
	}

	if stored := sizeOf(t, a, id); stored.VCPU != before.VCPU {
		t.Fatalf("vcpu = %d, want the refused resize not to have been written", stored.VCPU)
	}

	if before.MemoryMiB > 256 {
		rec, _ = resize(t, a, id, `{"memory_mib":256}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("shrinking was refused: %d %s: a shrink claims nothing",
				rec.Code, rec.Body.String())
		}
	}
}

func TestResizingSomebodyElsesInstanceIsNotFound(t *testing.T) {
	a, _ := newBalancingApp(t)
	id := newInstance(t, a, a.secret, "web")

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, other, http.MethodPost, "/v1/instances/"+id+"/resize",
		strings.NewReader(`{"vcpu":2}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: a 403 would confirm the id exists",
			rec.Code, http.StatusNotFound)
	}
}
