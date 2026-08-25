package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runCLI(t *testing.T, endpoint string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--endpoint", endpoint}, args...))

	err := root.Execute()
	return out.String(), err
}

func fakeControlPlane(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestInstanceCreateSendsFlagsAsJSON(t *testing.T) {
	var received map[string]any

	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/instances" {
			t.Errorf("got %s %s, want POST /v1/instances", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(instanceView{
			ID: "i-abc", Name: "api-1", Isolation: "container", Image: "alpine:3.20",
			VCPU: 2, MemoryMiB: 1024, Desired: "running", Observed: "pending",
		})
	})

	out, err := runCLI(t, endpoint, "instance", "create",
		"--name", "api-1", "--image", "alpine:3.20", "--vcpu", "2", "--memory-mib", "1024")
	if err != nil {
		t.Fatalf("unexpected error: %v (out: %s)", err, out)
	}

	if received["name"] != "api-1" {
		t.Errorf("name = %v, want api-1", received["name"])
	}
	if received["isolation"] != "container" {
		t.Errorf("isolation = %v, want the container default", received["isolation"])
	}
	if received["vcpu"] != float64(2) {
		t.Errorf("vcpu = %v, want 2", received["vcpu"])
	}
	if !strings.Contains(out, "api-1") || !strings.Contains(out, "i-abc") {
		t.Errorf("output does not show the created instance: %s", out)
	}
}

func TestInstanceCreateOmitsUnsetSizes(t *testing.T) {
	var received map[string]any

	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(instanceView{ID: "i-abc", Name: "api-1"})
	})

	if _, err := runCLI(t, endpoint, "instance", "create",
		"--name", "api-1", "--image", "alpine:3.20"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, present := received["vcpu"]; present {
		t.Error("vcpu was sent even though the flag was unset; the server default is bypassed")
	}
	if _, present := received["memory_mib"]; present {
		t.Error("memory_mib was sent even though the flag was unset")
	}
}

func TestInstanceCreateRequiresNameAndImage(t *testing.T) {
	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the control plane must not be called when required flags are missing")
	})

	if _, err := runCLI(t, endpoint, "instance", "create"); err == nil {
		t.Fatal("expected an error when --name and --image are missing")
	}
}

func TestInstanceListRendersTable(t *testing.T) {
	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(instanceListView{Instances: []instanceView{
			{ID: "i-abc", Name: "api-1", Isolation: "container", Image: "alpine", VCPU: 1, MemoryMiB: 512, Desired: "running", Observed: "pending"},
		}})
	})

	out, err := runCLI(t, endpoint, "instance", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"NAME", "OBSERVED", "RESTARTS", "api-1", "1cpu/512Mi", "-"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestInstanceListRendersJSON(t *testing.T) {
	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(instanceListView{Instances: []instanceView{{ID: "i-abc", Name: "api-1"}}})
	})

	out, err := runCLI(t, endpoint, "instance", "list", "--output", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed instanceListView
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, out)
	}
	if len(parsed.Instances) != 1 || parsed.Instances[0].ID != "i-abc" {
		t.Fatalf("parsed = %+v, want one instance i-abc", parsed.Instances)
	}
}

func TestEmptyListIsReported(t *testing.T) {
	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(instanceListView{Instances: []instanceView{}})
	})

	out, err := runCLI(t, endpoint, "instance", "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "no results") {
		t.Fatalf("output = %q, want a no-results message", out)
	}
}

func TestUnknownOutputFormatFails(t *testing.T) {
	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(instanceListView{Instances: []instanceView{{ID: "i-abc"}}})
	})

	if _, err := runCLI(t, endpoint, "instance", "list", "--output", "yaml"); err == nil {
		t.Fatal("expected an error for an unknown output format")
	}
}

func TestAPIErrorIsReportedWithItsCode(t *testing.T) {
	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":{"code":"instance_name_taken","message":"an instance with that name already exists"}}`))
	})

	_, err := runCLI(t, endpoint, "instance", "create", "--name", "api-1", "--image", "alpine")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "instance_name_taken") {
		t.Fatalf("error = %q, want it to carry the API error code", err)
	}
}

func TestStopPostsToTheTransitionEndpoint(t *testing.T) {
	var path string

	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(instanceView{ID: "i-abc", Name: "api-1", Desired: "stopped"})
	})

	if _, err := runCLI(t, endpoint, "instance", "stop", "i-abc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "POST /v1/instances/i-abc/stop"; path != want {
		t.Fatalf("request = %q, want %q", path, want)
	}
}

func TestDeleteAcceptsNoContent(t *testing.T) {
	endpoint := fakeControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	out, err := runCLI(t, endpoint, "instance", "delete", "i-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "deleted i-abc") {
		t.Fatalf("output = %q, want a deletion confirmation", out)
	}
}
