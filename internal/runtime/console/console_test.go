package console

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeVMM(t *testing.T, dir string) net.Conn {
	t.Helper()

	listener, err := net.Listen("unix", filepath.Join(dir, UpstreamName))
	if err != nil {
		t.Fatalf("listen as the vmm: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	select {
	case conn := <-accepted:
		t.Cleanup(func() { conn.Close() })
		return conn
	case <-time.After(3 * time.Second):
		t.Fatal("the hub never connected to the serial socket")
		return nil
	}
}

func read(t *testing.T, conn net.Conn) string {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	buffer := make([]byte, 256)
	count, err := conn.Read(buffer)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buffer[:count])
}

func TestHubFansOut(t *testing.T) {
	dir := t.TempDir()

	done := make(chan *Hub, 1)
	go func() {
		hub, err := Attach(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Errorf("attach: %v", err)
			done <- nil
			return
		}
		done <- hub
	}()

	guest := fakeVMM(t, dir)
	hub := <-done
	if hub == nil {
		t.Fatal("the hub never came up")
	}
	t.Cleanup(func() { hub.Close() })

	if _, err := guest.Write([]byte("before anyone attaches\n")); err != nil {
		t.Fatalf("guest write: %v", err)
	}
	waitFor(t, filepath.Join(dir, LogName), "before anyone attaches")

	first, err := net.Dial("unix", filepath.Join(dir, AttachName))
	if err != nil {
		t.Fatalf("first client: %v", err)
	}
	defer first.Close()

	second, err := net.Dial("unix", filepath.Join(dir, AttachName))
	if err != nil {
		t.Fatalf("second client: %v", err)
	}
	defer second.Close()

	if _, err := guest.Write([]byte("login:")); err != nil {
		t.Fatalf("guest write: %v", err)
	}

	if got := read(t, first); got != "login:" {
		t.Fatalf("first client saw %q", got)
	}
	if got := read(t, second); got != "login:" {
		t.Fatalf("second client saw %q, so the hub is not fanning out", got)
	}

	if _, err := first.Write([]byte("root\n")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if got := read(t, guest); got != "root\n" {
		t.Fatalf("the guest saw %q from the client", got)
	}

	waitFor(t, filepath.Join(dir, LogName), "login:")
}

func waitFor(t *testing.T, path, want string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the console log never recorded %q", want)
}

func TestFindReportsMissingConsole(t *testing.T) {
	root := t.TempDir()

	if _, err := Find(root, "i-nope"); err != ErrNoConsole {
		t.Fatalf("want ErrNoConsole, got %v", err)
	}

	dir := filepath.Join(root, "microvms", "i-yes")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, AttachName), nil, 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	found, err := Find(root, "i-yes")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found != filepath.Join(dir, AttachName) {
		t.Fatalf("found %q", found)
	}
}
