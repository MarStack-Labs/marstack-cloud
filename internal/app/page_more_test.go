package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type anyPage struct {
	Volumes   []struct{ ID string } `json:"volumes"`
	Backups   []struct{ ID string } `json:"backups"`
	Snapshots []struct{ ID string } `json:"snapshots"`
	Next      string                `json:"next"`
}

func (p anyPage) ids() []string {
	out := make([]string, 0, 8)
	for _, v := range p.Volumes {
		out = append(out, v.ID)
	}
	for _, b := range p.Backups {
		out = append(out, b.ID)
	}
	for _, s := range p.Snapshots {
		out = append(out, s.ID)
	}
	return out
}

func readAnyPage(t *testing.T, a *testApp, path, query string) anyPage {
	t.Helper()

	rec := do(t, a, http.MethodGet, path+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s%s: %d %s", path, query, rec.Code, rec.Body.String())
	}

	var body anyPage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func walk(t *testing.T, a *testApp, path string, limit int) []string {
	t.Helper()

	seen := make([]string, 0, 16)
	query := fmt.Sprintf("?limit=%d", limit)

	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatalf("%s: the cursor never ran out, so paging does not terminate", path)
		}

		got := readAnyPage(t, a, path, query)
		seen = append(seen, got.ids()...)
		if got.Next == "" {
			return seen
		}
		if len(got.ids()) < limit {
			t.Fatalf("%s: a page of %d rows under a limit of %d still carried a cursor, "+
				"so every client pays for an empty request", path, len(got.ids()), limit)
		}
		query = fmt.Sprintf("?limit=%d&after=%s", limit, got.Next)
	}
}

func distinct(t *testing.T, path string, ids []string, want int) {
	t.Helper()

	unique := map[string]int{}
	for _, id := range ids {
		unique[id]++
	}
	for id, times := range unique {
		if times != 1 {
			t.Fatalf("%s: %s came back %d times", path, id, times)
		}
	}
	if len(unique) != want {
		t.Fatalf("%s: walked %d rows, want %d", path, len(unique), want)
	}
}

func seedVolumes(t *testing.T, a *testApp, count int) []string {
	t.Helper()

	ids := make([]string, 0, count)
	for i := range count {
		ids = append(ids, createIn(t, a, a.secret, "/v1/volumes",
			fmt.Sprintf(`{"name":"data-%c","size_gib":1}`, 'a'+i)))
	}
	return ids
}

func TestVolumesWalkEveryPage(t *testing.T) {
	a, _ := newBalancingApp(t)
	made := seedVolumes(t, a, 7)

	distinct(t, "/v1/volumes", walk(t, a, "/v1/volumes", 3), len(made))
}

func attachedVolume(t *testing.T, a *testApp, nodeID, name string) (string, string) {
	t.Helper()

	id := createIn(t, a, a.secret, "/v1/volumes",
		fmt.Sprintf(`{"name":%q,"size_gib":1}`, name))

	instance := createIn(t, a, a.secret, "/v1/instances",
		fmt.Sprintf(`{"name":"%s-host","isolation":"vm","image":"ubuntu-24.04"}`, name))
	if placed := waitForPlacement(t, a, instance); placed == "" {
		t.Fatalf("instance %s-host was never placed on a node", name)
	}

	status := do(t, a, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instance+"/status",
		strings.NewReader(`{"observed_state":"running"}`))
	if status.Code != http.StatusOK {
		t.Fatalf("report running: %d %s", status.Code, status.Body.String())
	}

	rec := do(t, a, http.MethodPost, "/v1/volumes/"+id+"/attach",
		strings.NewReader(fmt.Sprintf(`{"instance_id":%q}`, instance)))
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
	}
	return id, instance
}

func stopInstance(t *testing.T, a *testApp, instance string) {
	t.Helper()

	rec := do(t, a, http.MethodPost, "/v1/instances/"+instance+"/stop", nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBackupsWalkEveryPage(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, _ := attachedVolume(t, a, nodeID, "backed")

	made := 0
	for i := range 7 {
		createIn(t, a, a.secret, "/v1/volumes/"+volume+"/backups",
			fmt.Sprintf(`{"name":"copy-%c"}`, 'a'+i))
		made++
	}

	distinct(t, "/v1/backups", walk(t, a, "/v1/backups", 3), made)
}

func TestSnapshotsWalkEveryPage(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, host := attachedVolume(t, a, nodeID, "snapped")
	stopInstance(t, a, host)

	made := 0
	for i := range 7 {
		createIn(t, a, a.secret, "/v1/volumes/"+volume+"/snapshots",
			fmt.Sprintf(`{"name":"point-%c"}`, 'a'+i))
		made++
	}

	distinct(t, "/v1/snapshots", walk(t, a, "/v1/snapshots", 3), made)
}

func TestEveryPagedListRefusesABadCursor(t *testing.T) {
	a, _ := newBalancingApp(t)

	paths := []string{"/v1/instances", "/v1/volumes", "/v1/backups", "/v1/snapshots"}
	queries := []string{"?limit=0", "?limit=-1", "?limit=many", "?after=nonsense!!"}

	for _, path := range paths {
		for _, query := range queries {
			rec := do(t, a, http.MethodGet, path+query, nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("GET %s%s: status = %d, want %d",
					path, query, rec.Code, http.StatusBadRequest)
			}
		}
	}
}

func TestEveryPagedListStaysInsideTheProject(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	volume, host := attachedVolume(t, a, nodeID, "shared")
	createIn(t, a, a.secret, "/v1/volumes/"+volume+"/backups", `{"name":"copy"}`)
	stopInstance(t, a, host)
	createIn(t, a, a.secret, "/v1/volumes/"+volume+"/snapshots", `{"name":"point"}`)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))

	for _, path := range []string{"/v1/volumes", "/v1/backups", "/v1/snapshots"} {
		rec := doAs(t, a, other, http.MethodGet, path+"?limit=100", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}

		var got anyPage
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if ids := got.ids(); len(ids) != 0 {
			t.Fatalf("GET %s returned %d rows from another project", path, len(ids))
		}
	}
}
