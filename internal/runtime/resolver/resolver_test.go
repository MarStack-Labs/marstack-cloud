package resolver

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

func query(name string, qtype uint16) []byte {
	msg := make([]byte, headerLen)
	binary.BigEndian.PutUint16(msg[0:2], 0x1234)
	binary.BigEndian.PutUint16(msg[2:4], flagRecursionOK)
	binary.BigEndian.PutUint16(msg[4:6], 1)

	for _, label := range strings.Split(name, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)

	msg = binary.BigEndian.AppendUint16(msg, qtype)
	return binary.BigEndian.AppendUint16(msg, classINET)
}

func newTestResolver(t *testing.T, records map[string]string) (*Resolver, string) {
	t.Helper()

	r := New(logging.New("error", io.Discard))
	r.Update(records)
	t.Cleanup(r.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := r.Listen(ctx, "127.0.0.1"); err != nil {
		t.Skipf("cannot bind the resolver in this environment: %v", err)
	}

	r.mu.RLock()
	conn := r.listeners["127.0.0.1"]
	r.mu.RUnlock()

	return r, conn.LocalAddr().String()
}

func ask(t *testing.T, addr string, msg []byte) []byte {
	t.Helper()

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial resolver: %v", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write query: %v", err)
	}

	buf := make([]byte, maxMessage)
	read, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return buf[:read]
}

func TestResolvesAKnownName(t *testing.T) {
	_, addr := newTestResolver(t, map[string]string{"web-1.default.internal": "10.20.0.65"})

	reply := ask(t, addr, query("web-1.default.internal", typeA))

	if binary.BigEndian.Uint16(reply[0:2]) != 0x1234 {
		t.Fatal("the reply does not carry the query id")
	}
	if binary.BigEndian.Uint16(reply[2:4])&flagResponse == 0 {
		t.Fatal("the response bit is not set")
	}
	if count := binary.BigEndian.Uint16(reply[6:8]); count != 1 {
		t.Fatalf("answer count = %d, want 1", count)
	}

	ip := net.IP(reply[len(reply)-4:]).String()
	if ip != "10.20.0.65" {
		t.Fatalf("answer = %s, want 10.20.0.65", ip)
	}
}

func TestResolvesCaseInsensitively(t *testing.T) {
	_, addr := newTestResolver(t, map[string]string{"web-1.default.internal": "10.20.0.65"})

	reply := ask(t, addr, query("WEB-1.Default.Internal", typeA))

	if count := binary.BigEndian.Uint16(reply[6:8]); count != 1 {
		t.Fatalf("answer count = %d, want 1: dns names are case insensitive", count)
	}
}

func TestUnknownNameInTheZoneIsNXDOMAIN(t *testing.T) {
	_, addr := newTestResolver(t, map[string]string{"web-1.default.internal": "10.20.0.65"})

	reply := ask(t, addr, query("nope.default.internal", typeA))

	if rcode := binary.BigEndian.Uint16(reply[2:4]) & 0x000F; rcode != rcodeNameError {
		t.Fatalf("rcode = %d, want %d", rcode, rcodeNameError)
	}
	if count := binary.BigEndian.Uint16(reply[6:8]); count != 0 {
		t.Fatalf("answer count = %d, want 0", count)
	}
}

func TestIPv6QueryForAKnownNameIsEmptyNotAnError(t *testing.T) {
	_, addr := newTestResolver(t, map[string]string{"web-1.default.internal": "10.20.0.65"})

	reply := ask(t, addr, query("web-1.default.internal", typeAAAA))

	if rcode := binary.BigEndian.Uint16(reply[2:4]) & 0x000F; rcode != 0 {
		t.Fatalf("rcode = %d, want 0: an NXDOMAIN here would make a resolver give up on the name", rcode)
	}
	if count := binary.BigEndian.Uint16(reply[6:8]); count != 0 {
		t.Fatalf("answer count = %d, want 0", count)
	}
}

func TestUpdateReplacesTheZone(t *testing.T) {
	r, addr := newTestResolver(t, map[string]string{"web-1.default.internal": "10.20.0.65"})

	r.Update(map[string]string{"web-2.default.internal": "10.20.0.66"})

	gone := ask(t, addr, query("web-1.default.internal", typeA))
	if rcode := binary.BigEndian.Uint16(gone[2:4]) & 0x000F; rcode != rcodeNameError {
		t.Fatal("a name removed from the zone still resolves")
	}

	fresh := ask(t, addr, query("web-2.default.internal", typeA))
	if count := binary.BigEndian.Uint16(fresh[6:8]); count != 1 {
		t.Fatal("a name added to the zone does not resolve")
	}
}

func TestUpdateIgnoresUnusableAddresses(t *testing.T) {
	r := New(logging.New("error", io.Discard))
	r.Update(map[string]string{
		"good.default.internal": "10.20.0.65",
		"bad.default.internal":  "not-an-address",
		"v6.default.internal":   "fd00::1",
	})

	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.records) != 1 {
		t.Fatalf("records = %v, want only the usable one", r.records)
	}
	if _, ok := r.records["good.default.internal"]; !ok {
		t.Fatal("the usable record was dropped")
	}
}

func TestListenIsIdempotent(t *testing.T) {
	r, _ := newTestResolver(t, nil)

	if err := r.Listen(context.Background(), "127.0.0.1"); err != nil {
		t.Fatalf("second Listen returned %v, want it to be a no-op", err)
	}
}

func TestMalformedQueryIsIgnored(t *testing.T) {
	_, addr := newTestResolver(t, nil)

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte{0x12, 0x34}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	buf := make([]byte, maxMessage)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("a malformed query produced a reply; it must be dropped")
	}
}

func TestDiscoverUpstreamsNeverReturnsLoopback(t *testing.T) {
	for _, server := range discoverUpstreams() {
		if strings.HasPrefix(server, "127.") {
			t.Fatalf("upstream %s is loopback: the resolver would forward to itself", server)
		}
	}
}
