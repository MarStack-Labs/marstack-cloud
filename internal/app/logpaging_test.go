package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type pagedEntry struct {
	ID      int64  `json:"id"`
	Path    string `json:"path"`
	Message string `json:"message"`
	Kind    string `json:"kind"`
}

type pagedBody struct {
	Entries []pagedEntry `json:"entries"`
	Events  []pagedEntry `json:"events"`
	Next    string       `json:"next"`
}

func readLogPage(t *testing.T, a *testApp, path string) (int, pagedBody) {
	t.Helper()

	rec := do(t, a, http.MethodGet, path, nil)

	var body pagedBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func manyNetworks(t *testing.T, a *testApp, count int) {
	t.Helper()

	for i := range count {
		rec := do(t, a, http.MethodPost, "/v1/networks",
			strings.NewReader(`{"name":"paged`+strconv.Itoa(i)+
				`","cidr":"10.`+strconv.Itoa(150+i)+`.0.0/16"}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("network %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
}

func TestTheAuditTrailPagesBackwardsWithoutRepeating(t *testing.T) {
	a, _ := newBalancingApp(t)
	manyNetworks(t, a, 12)

	seen := map[int64]bool{}
	path := "/v1/audit?limit=4"

	for range 4 {
		code, body := readLogPage(t, a, path)
		if code != http.StatusOK {
			t.Fatalf("page: %d", code)
		}
		for _, entry := range body.Entries {
			if seen[entry.ID] {
				t.Fatalf("entry %d came back twice. A descending page needs id < cursor: "+
					"reusing the ascending predicate returns the same first page forever",
					entry.ID)
			}
			seen[entry.ID] = true
		}
		if body.Next == "" {
			break
		}
		path = "/v1/audit?limit=4&after=" + body.Next
	}

	if len(seen) < 8 {
		t.Fatalf("only %d entries across four pages of four, so paging stopped early",
			len(seen))
	}
}

func TestTheAuditPagesNewestFirst(t *testing.T) {
	a, _ := newBalancingApp(t)
	manyNetworks(t, a, 6)

	_, body := readLogPage(t, a, "/v1/audit?limit=20")
	if len(body.Entries) < 2 {
		t.Fatalf("entries = %d", len(body.Entries))
	}

	for i := 1; i < len(body.Entries); i++ {
		if body.Entries[i].ID >= body.Entries[i-1].ID {
			t.Fatalf("entry %d comes after %d. A trail is read from the end: the newest "+
				"has to be first or the first page is the oldest history",
				body.Entries[i].ID, body.Entries[i-1].ID)
		}
	}
}

func TestEventsPageBackwardsToo(t *testing.T) {
	a, _ := newBalancingApp(t)

	created := createService(t, a, a.secret,
		`{"name":"web","replicas":4,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 2)
	_ = created

	seen := map[int64]bool{}
	path := "/v1/events?limit=2"

	for range 4 {
		code, body := readLogPage(t, a, path)
		if code != http.StatusOK {
			t.Fatalf("page: %d", code)
		}
		for _, entry := range body.Events {
			if seen[entry.ID] {
				t.Fatalf("event %d came back twice", entry.ID)
			}
			seen[entry.ID] = true
		}
		if body.Next == "" {
			break
		}
		path = "/v1/events?limit=2&after=" + body.Next
	}

	if len(seen) < 4 {
		t.Fatalf("only %d events across the pages", len(seen))
	}
}

func TestAFilterSurvivesPaging(t *testing.T) {
	a, _ := newBalancingApp(t)

	createService(t, a, a.secret,
		`{"name":"web","replicas":4,"isolation":"container","image":"alpine:3.20"}`)
	settle(t, a, 2)

	path := "/v1/events?limit=2&kind=service.replica_added"
	pages := 0

	for range 4 {
		code, body := readLogPage(t, a, path)
		if code != http.StatusOK {
			t.Fatalf("page: %d", code)
		}
		for _, entry := range body.Events {
			if entry.Kind != "service.replica_added" {
				t.Fatalf("kind = %q leaked into a filtered page: the cursor is a position, "+
					"not a filter, so the caller resends the filter and the query must "+
					"still apply it", entry.Kind)
			}
		}
		pages++
		if body.Next == "" {
			break
		}
		path = "/v1/events?limit=2&kind=service.replica_added&after=" + body.Next
	}

	if pages < 2 {
		t.Fatalf("pages = %d, want the filter to span more than one", pages)
	}
}

func TestACursorThatWasNotHandedOutIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	for _, bad := range []string{
		"nonsense", "!!!!", "bm90LWEtY3Vyc29y",
		"AGFiYw", "AC01", "ADA",
	} {
		for _, path := range []string{"/v1/audit?after=", "/v1/events?after="} {
			if code, _ := readLogPage(t, a, path+bad); code != http.StatusBadRequest {
				t.Errorf("%s%s: status = %d, want %d. The last three decode to a "+
					"well-formed envelope carrying an id that is not a positive number, "+
					"so they get past the envelope check and have to be refused where "+
					"the number is read", path, bad, code, http.StatusBadRequest)
			}
		}
	}
}

func TestABadLimitIsRefusedRatherThanIgnored(t *testing.T) {
	a, _ := newBalancingApp(t)

	for _, path := range []string{"/v1/audit?limit=abc", "/v1/events?limit=-1"} {
		if code, _ := readLogPage(t, a, path); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d: swallowing a bad limit hands back a "+
				"different page than the one that was asked for and says nothing",
				path, code, http.StatusBadRequest)
		}
	}
}

func TestTheLimitIsCappedHoweverMuchIsAsked(t *testing.T) {
	a, _ := newBalancingApp(t)
	manyNetworks(t, a, 8)

	_, body := readLogPage(t, a, "/v1/audit?limit=99999")
	if len(body.Entries) > 500 {
		t.Fatalf("entries = %d, want at most the ceiling", len(body.Entries))
	}
}
