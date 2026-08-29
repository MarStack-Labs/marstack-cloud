package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

type instancePage struct {
	Instances []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"instances"`
	Next string `json:"next"`
}

func readPage(t *testing.T, a *testApp, query string) instancePage {
	t.Helper()

	rec := do(t, a, http.MethodGet, "/v1/instances"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list%s: %d %s", query, rec.Code, rec.Body.String())
	}

	var body instancePage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func makeInstances(t *testing.T, a *testApp, count int) []string {
	t.Helper()

	ids := make([]string, 0, count)
	for i := range count {
		ids = append(ids, newInstance(t, a, a.secret, "web-"+string(rune('a'+i))))
	}
	return ids
}

func TestAListWalksEveryPageWithoutRepeatingOrSkipping(t *testing.T) {
	a, _ := newBalancingApp(t)
	made := makeInstances(t, a, 7)

	seen := map[string]int{}
	query := "?limit=3"
	pages := 0

	for {
		got := readPage(t, a, query)
		pages++
		for _, in := range got.Instances {
			seen[in.ID]++
		}
		if got.Next == "" {
			break
		}
		if pages > 10 {
			t.Fatal("the cursor never ran out, so paging does not terminate")
		}
		query = "?limit=3&after=" + got.Next
	}

	if len(seen) != len(made) {
		t.Fatalf("saw %d instances across %d pages, want %d", len(seen), pages, len(made))
	}
	for id, times := range seen {
		if times != 1 {
			t.Fatalf("%s came back %d times", id, times)
		}
	}
	if pages < 3 {
		t.Fatalf("pages = %d, want seven instances at three a page to take three", pages)
	}
}

func TestAPageStopsAtItsLimit(t *testing.T) {
	a, _ := newBalancingApp(t)
	makeInstances(t, a, 5)

	got := readPage(t, a, "?limit=2")
	if len(got.Instances) != 2 {
		t.Fatalf("instances = %d, want 2", len(got.Instances))
	}
	if got.Next == "" {
		t.Fatal("a full page came back with no cursor, so the rest is unreachable")
	}
}

func TestTheLastPageCarriesNoCursor(t *testing.T) {
	a, _ := newBalancingApp(t)
	makeInstances(t, a, 3)

	got := readPage(t, a, "?limit=100")
	if len(got.Instances) != 3 {
		t.Fatalf("instances = %d, want 3", len(got.Instances))
	}
	if got.Next != "" {
		t.Fatalf("next = %q, want nothing left to fetch", got.Next)
	}
}

func TestABadPageRequestIsRefused(t *testing.T) {
	a, _ := newBalancingApp(t)

	for _, query := range []string{"?limit=0", "?limit=-1", "?limit=many", "?after=nonsense!!"} {
		rec := do(t, a, http.MethodGet, "/v1/instances"+query, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d", query, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestPagingStaysInsideTheProject(t *testing.T) {
	a, _ := newBalancingApp(t)
	makeInstances(t, a, 3)

	other := tokenIn(t, a, "outsider", newProject(t, a, "other"))
	rec := doAs(t, a, other, http.MethodGet, "/v1/instances?limit=100", nil)

	var got instancePage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Instances) != 0 {
		t.Fatalf("instances = %d, want none from another project", len(got.Instances))
	}
}
