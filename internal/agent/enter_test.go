package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNothingWaitingIsNotAnError(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/n-1/exec" {
			t.Errorf("path = %s", r.URL.Path)
		}
		asked++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret", nil)

	waiting, found, err := c.takeCommand(context.Background(), "n-1")
	if err != nil {
		t.Fatalf("take: %v, want no error: a node with nothing to run is the steady state, "+
			"so an error there is a warning every two seconds on every idle node", err)
	}
	if found {
		t.Fatalf("found = %+v, want nothing", waiting)
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want one", asked)
	}
}

func TestAnEmptyBodyOnTwoHundredIsStillAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret", nil)

	if _, _, err := c.takeCommand(context.Background(), "n-1"); err == nil {
		t.Fatal("a 200 promising a body and sending none was accepted, so a truncated " +
			"answer now reads as 'nothing to do'")
	}
}
