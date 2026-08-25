//go:build linux

package container

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("the container runtime needs root")
	}
}

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	return New(t.TempDir(), logging.New("error", io.Discard))
}

func stageBusyboxImage(t *testing.T, r *Runtime, reference string) {
	t.Helper()

	busybox, err := os.ReadFile("/usr/bin/busybox")
	if err != nil {
		t.Skipf("no busybox on this host to build an image from: %v", err)
	}

	if err := os.MkdirAll(r.layout.images(), 0o750); err != nil {
		t.Fatalf("create image directory: %v", err)
	}

	var raw bytes.Buffer
	archive := tar.NewWriter(&raw)

	for _, dir := range []string{"bin", "proc"} {
		if err := archive.WriteHeader(&tar.Header{
			Name: dir + "/", Mode: 0o755, Typeflag: tar.TypeDir,
		}); err != nil {
			t.Fatalf("write dir header: %v", err)
		}
	}

	if err := archive.WriteHeader(&tar.Header{
		Name: "bin/busybox", Mode: 0o755, Size: int64(len(busybox)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write busybox header: %v", err)
	}
	if _, err := archive.Write(busybox); err != nil {
		t.Fatalf("write busybox: %v", err)
	}

	for _, link := range []string{"sh", "sleep", "true", "false"} {
		if err := archive.WriteHeader(&tar.Header{
			Name: "bin/" + link, Linkname: "busybox", Typeflag: tar.TypeSymlink, Mode: 0o777,
		}); err != nil {
			t.Fatalf("write link header: %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	if _, err := writer.Write(raw.Bytes()); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	writer.Close()

	path := filepath.Join(r.layout.images(), imageFileName(reference)+".tar.gz")
	if err := os.WriteFile(path, zipped.Bytes(), 0o640); err != nil {
		t.Fatalf("write image: %v", err)
	}
}

func specFor(instanceID string, command ...string) workload.Spec {
	return workload.Spec{
		InstanceID: instanceID,
		Name:       "test-" + instanceID,
		Isolation:  "container",
		Image:      "busybox:test",
		Command:    command,
		VCPU:       1,
		MemoryMiB:  128,
	}
}

func waitForPhase(t *testing.T, r *Runtime, instanceID string, want workload.Phase) workload.State {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	var last workload.State

	for time.Now().Before(deadline) {
		state, err := r.Status(context.Background(), instanceID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		last = state
		if state.Phase == want {
			return state
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("phase = %q after waiting, want %q (message: %s)", last.Phase, want, last.Message)
	return last
}

func TestStartRunsAWorkloadInItsOwnNamespaces(t *testing.T) {
	requireRoot(t)

	r := newTestRuntime(t)
	stageBusyboxImage(t, r, "busybox:test")

	spec := specFor("i-namespaces", "/bin/sh", "-c", "sleep 30")
	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { r.Remove(context.Background(), spec.InstanceID) })

	waitForPhase(t, r, spec.InstanceID, workload.PhaseRunning)

	pid, alive := r.livePID(spec.InstanceID)
	if !alive {
		t.Fatal("no live pid was recorded")
	}

	for _, namespace := range []string{"pid", "mnt", "net", "uts", "ipc"} {
		host, err := os.Readlink("/proc/1/ns/" + namespace)
		if err != nil {
			t.Fatalf("read host %s namespace: %v", namespace, err)
		}
		mine, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/ns/" + namespace)
		if err != nil {
			t.Fatalf("read workload %s namespace: %v", namespace, err)
		}
		if host == mine {
			t.Errorf("%s namespace is shared with the host: %s", namespace, mine)
		}
	}
}

func TestCgroupCarriesTheRequestedLimits(t *testing.T) {
	requireRoot(t)

	r := newTestRuntime(t)
	stageBusyboxImage(t, r, "busybox:test")

	spec := specFor("i-limits", "/bin/sh", "-c", "sleep 30")
	spec.MemoryMiB = 256
	spec.VCPU = 2

	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { r.Remove(context.Background(), spec.InstanceID) })

	waitForPhase(t, r, spec.InstanceID, workload.PhaseRunning)

	limits := map[string]string{
		"memory.max": "268435456",
		"cpu.max":    "200000 100000",
	}
	for file, want := range limits {
		raw, err := os.ReadFile(filepath.Join(r.layout.cgroup(spec.InstanceID), file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if got := strings.TrimSpace(string(raw)); got != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
}

func TestExitCodeIsReportedFaithfully(t *testing.T) {
	requireRoot(t)

	r := newTestRuntime(t)
	stageBusyboxImage(t, r, "busybox:test")

	spec := specFor("i-exit", "/bin/sh", "-c", "exit 7")
	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { r.Remove(context.Background(), spec.InstanceID) })

	state := waitForPhase(t, r, spec.InstanceID, workload.PhaseExited)
	if state.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7 (message: %s)", state.ExitCode, state.Message)
	}
}

func TestStatusIsSafeUnderConcurrentReaping(t *testing.T) {
	requireRoot(t)

	r := newTestRuntime(t)
	stageBusyboxImage(t, r, "busybox:test")

	spec := specFor("i-race", "/bin/sh", "-c", "exit 3")
	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { r.Remove(context.Background(), spec.InstanceID) })

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 200 {
				if _, err := r.Status(context.Background(), spec.InstanceID); err != nil {
					t.Errorf("status: %v", err)
					return
				}
			}
		}()
	}
	wait.Wait()

	state := waitForPhase(t, r, spec.InstanceID, workload.PhaseExited)
	if state.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", state.ExitCode)
	}
}

func TestRemoveClearsCgroupAndFiles(t *testing.T) {
	requireRoot(t)

	r := newTestRuntime(t)
	stageBusyboxImage(t, r, "busybox:test")

	spec := specFor("i-remove", "/bin/sh", "-c", "sleep 30")
	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForPhase(t, r, spec.InstanceID, workload.PhaseRunning)

	if err := r.Remove(context.Background(), spec.InstanceID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := os.Stat(r.layout.instance(spec.InstanceID)); !os.IsNotExist(err) {
		t.Error("the instance directory survived removal")
	}
	if _, err := os.Stat(r.layout.cgroup(spec.InstanceID)); !os.IsNotExist(err) {
		t.Error("the cgroup survived removal")
	}

	state, err := r.Status(context.Background(), spec.InstanceID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if state.Phase != workload.PhaseAbsent {
		t.Fatalf("phase = %q after removal, want %q", state.Phase, workload.PhaseAbsent)
	}
}

func TestStartIsIdempotentForARunningWorkload(t *testing.T) {
	requireRoot(t)

	r := newTestRuntime(t)
	stageBusyboxImage(t, r, "busybox:test")

	spec := specFor("i-twice", "/bin/sh", "-c", "sleep 30")
	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("first start: %v", err)
	}
	t.Cleanup(func() { r.Remove(context.Background(), spec.InstanceID) })

	waitForPhase(t, r, spec.InstanceID, workload.PhaseRunning)
	first, _ := r.livePID(spec.InstanceID)

	if err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("second start: %v", err)
	}

	second, _ := r.livePID(spec.InstanceID)
	if first != second {
		t.Fatalf("pid changed from %d to %d: a second start replaced a running workload", first, second)
	}
}

func TestMissingImageFailsWithAUsefulMessage(t *testing.T) {
	requireRoot(t)

	r := newTestRuntime(t)

	spec := specFor("i-noimage", "/bin/sh", "-c", "true")
	err := r.Start(context.Background(), spec)

	if err == nil {
		t.Fatal("expected an error for an image that is not present")
	}
	if !strings.Contains(err.Error(), "busybox") {
		t.Fatalf("error = %v, want it to name the image", err)
	}
}
