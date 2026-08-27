package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type quotaBody struct {
	ProjectID string `json:"project_id"`
	Limits    struct {
		Instances int `json:"instances"`
		VCPU      int `json:"vcpu"`
		MemoryMiB int `json:"memory_mib"`
		Volumes   int `json:"volumes"`
		VolumeGiB int `json:"volume_gib"`
	} `json:"limits"`
	Used struct {
		Instances int `json:"instances"`
		VCPU      int `json:"vcpu"`
		MemoryMiB int `json:"memory_mib"`
		Volumes   int `json:"volumes"`
		VolumeGiB int `json:"volume_gib"`
	} `json:"used"`
}

func setQuota(t *testing.T, a *testApp, projectID, limits string) {
	t.Helper()

	rec := do(t, a, http.MethodPut, "/v1/quotas/"+projectID, strings.NewReader(limits))
	if rec.Code != http.StatusOK {
		t.Fatalf("set quota: %d %s", rec.Code, rec.Body.String())
	}
}

func readQuota(t *testing.T, a *testApp, secret string) quotaBody {
	t.Helper()

	rec := doAs(t, a, secret, http.MethodGet, "/v1/quota", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read quota: %d %s", rec.Code, rec.Body.String())
	}

	var body quotaBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestWithoutALimitAProjectIsUnlimited(t *testing.T) {
	a := newTestApp(t)

	for i := range 4 {
		newInstance(t, a, a.secret, "web-"+string(rune('a'+i)))
	}

	q := readQuota(t, a, a.secret)
	if q.Limits.Instances != 0 {
		t.Fatalf("limit = %d, want 0 meaning unlimited", q.Limits.Instances)
	}
	if q.Used.Instances != 4 {
		t.Fatalf("used = %d, want the four that exist", q.Used.Instances)
	}
}

func TestAnInstanceOverTheCountLimitIsRefused(t *testing.T) {
	a := newTestApp(t)
	setQuota(t, a, "prj-default", `{"instances":2}`)

	newInstance(t, a, a.secret, "one")
	newInstance(t, a, a.secret, "two")

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"three","isolation":"container","image":"alpine:3.20"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "quota_exceeded" {
		t.Fatalf("code = %q", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "2") {
		t.Fatalf("message = %q, want it to name the limit", body.Error.Message)
	}
}

func TestAnInstanceOverTheMemoryLimitIsRefused(t *testing.T) {
	a := newTestApp(t)
	setQuota(t, a, "prj-default", `{"memory_mib":1024}`)

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/instances", strings.NewReader(
		`{"name":"big","isolation":"container","image":"alpine:3.20","memory_mib":2048}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: 2048 MiB does not fit in 1024", rec.Code, http.StatusConflict)
	}

	rec = doAs(t, a, a.secret, http.MethodPost, "/v1/instances", strings.NewReader(
		`{"name":"small","isolation":"container","image":"alpine:3.20","memory_mib":512}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want the one that fits to be created: %s", rec.Code, rec.Body.String())
	}
}

func TestAVolumeOverTheCapacityLimitIsRefused(t *testing.T) {
	a := newTestApp(t)
	setQuota(t, a, "prj-default", `{"volume_gib":10}`)

	createIn(t, a, a.secret, "/v1/volumes", `{"name":"first","size_gib":8}`)

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/volumes",
		strings.NewReader(`{"name":"second","size_gib":5}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: 8 + 5 exceeds 10", rec.Code, http.StatusConflict)
	}

	rec = doAs(t, a, a.secret, http.MethodPost, "/v1/volumes",
		strings.NewReader(`{"name":"second","size_gib":2}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want the one that fits: %s", rec.Code, rec.Body.String())
	}
}

func TestDeletingWorkGivesTheQuotaBack(t *testing.T) {
	a := newTestApp(t)
	setQuota(t, a, "prj-default", `{"instances":1}`)

	id := newInstance(t, a, a.secret, "only")

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"second","isolation":"container","image":"alpine:3.20"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want the limit to bite", rec.Code)
	}

	if rec := doAs(t, a, a.secret, http.MethodDelete, "/v1/instances/"+id, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	newInstance(t, a, a.secret, "second")
}

func TestOneProjectsLimitDoesNotBindAnother(t *testing.T) {
	a := newTestApp(t)

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	setQuota(t, a, "prj-default", `{"instances":1}`)
	newInstance(t, a, a.secret, "ours")

	for i := range 3 {
		newInstance(t, a, theirs, "theirs-"+string(rune('a'+i)))
	}
}

func TestAQuotaCanOnlyBeSetOnAProjectThatExists(t *testing.T) {
	a := newTestApp(t)

	rec := do(t, a, http.MethodPut, "/v1/quotas/prj-nope", strings.NewReader(`{"instances":1}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: a typo would silently limit nothing",
			rec.Code, http.StatusNotFound)
	}
}

func TestRemovingAQuotaMakesTheProjectUnlimitedAgain(t *testing.T) {
	a := newTestApp(t)
	setQuota(t, a, "prj-default", `{"instances":1}`)
	newInstance(t, a, a.secret, "only")

	rec := doAs(t, a, a.secret, http.MethodPost, "/v1/instances",
		strings.NewReader(`{"name":"second","isolation":"container","image":"alpine:3.20"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want the limit to bite first", rec.Code)
	}

	if rec := do(t, a, http.MethodDelete, "/v1/quotas/prj-default", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete quota: %d %s", rec.Code, rec.Body.String())
	}

	newInstance(t, a, a.secret, "second")
}

func TestAMemberSeesItsQuotaButCannotChangeIt(t *testing.T) {
	a := newTestApp(t)
	secret := memberToken(t, a)
	setQuota(t, a, "prj-default", `{"instances":5}`)

	q := readQuota(t, a, secret)
	if q.Limits.Instances != 5 {
		t.Fatalf("limit = %d, want the member to see what binds it", q.Limits.Instances)
	}

	rec := doAs(t, a, secret, http.MethodPut, "/v1/quotas/prj-default",
		strings.NewReader(`{"instances":500}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: raising your own limit is not a member's call",
			rec.Code, http.StatusForbidden)
	}
}

func TestANodeTokenCannotReadQuotas(t *testing.T) {
	a := newTestApp(t)
	secret := nodeToken(t, a)

	for _, path := range []string{"/v1/quota", "/v1/quotas"} {
		t.Run(path, func(t *testing.T) {
			if rec := doAs(t, a, secret, http.MethodGet, path, nil); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
			}
		})
	}
}

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIL0l2S7DDA2xhJmCJ8+eVQZlP4kzHJqPrGm0k" +
	"XlbLK9M umar@laptop"

func addKey(t *testing.T, a *testApp, name string) {
	t.Helper()

	body := `{"name":"` + name + `","public_key":"` + testKey + `"}`
	rec := do(t, a, http.MethodPost, "/v1/keys", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAVMCarriesTheKeysItWasCreatedWith(t *testing.T) {
	a := newTestApp(t)
	addKey(t, a, "laptop")

	body := `{"name":"box","isolation":"vm","image":"ubuntu-24.04","keys":["laptop"]}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		SSHKeys []string `json:"ssh_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(created.SSHKeys) != 1 || created.SSHKeys[0] != testKey {
		t.Fatalf("keys = %v, want the key material so the node needs no second lookup",
			created.SSHKeys)
	}
}

func TestAKeyOnSomethingWithoutCloudInitIsRefused(t *testing.T) {
	a := newTestApp(t)
	addKey(t, a, "laptop")

	for _, isolation := range []string{"container", "microvm", "sandbox"} {
		t.Run(isolation, func(t *testing.T) {
			body := `{"name":"box-` + isolation + `","isolation":"` + isolation +
				`","image":"alpine:3.20","keys":["laptop"]}`
			rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: a key accepted and never installed is worse "+
					"than one refused", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestAnUnknownKeyNameIsRefused(t *testing.T) {
	a := newTestApp(t)

	body := `{"name":"box","isolation":"vm","image":"ubuntu-24.04","keys":["nope"]}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a typo would boot a machine nobody can log in to",
			rec.Code, http.StatusBadRequest)
	}
}

func TestKeysAreScopedToTheirProject(t *testing.T) {
	a := newTestApp(t)
	addKey(t, a, "laptop")

	other := newProject(t, a, "tenant-b")
	theirs := tokenIn(t, a, "b-dev", other)

	if got := countAt(t, a, theirs, "/v1/keys", "keys"); got != 0 {
		t.Fatalf("tenant-b sees %d keys, want none", got)
	}

	body := `{"name":"box","isolation":"vm","image":"ubuntu-24.04","keys":["laptop"]}`
	rec := doAs(t, a, theirs, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: another project's key must not be installable",
			rec.Code, http.StatusBadRequest)
	}
}

func TestTheSameKeyNamedTwiceIsInstalledOnce(t *testing.T) {
	a := newTestApp(t)
	addKey(t, a, "laptop")
	addKey(t, a, "same-key-other-name")

	body := `{"name":"box","isolation":"vm","image":"ubuntu-24.04",` +
		`"keys":["laptop","same-key-other-name","laptop"]}`
	rec := do(t, a, http.MethodPost, "/v1/instances", strings.NewReader(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var created struct {
		SSHKeys []string `json:"ssh_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(created.SSHKeys) != 1 {
		t.Fatalf("keys = %v, want one line: authorized_keys with duplicates is noise",
			created.SSHKeys)
	}
}
