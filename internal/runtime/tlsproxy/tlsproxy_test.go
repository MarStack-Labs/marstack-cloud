package tlsproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func selfSigned(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "balancer.test"},
		DNSNames:     []string{"balancer.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	blob, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: blob})
	return string(certPEM), string(keyPEM)
}

func backend(t *testing.T, reply string) (string, int) {
	return backendOn(t, "127.0.0.1", 0, reply)
}

func backendOn(t *testing.T, host string, port int, reply string) (string, int) {
	t.Helper()

	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Skipf("cannot listen on %s:%d here: %v", host, port, err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.WriteString(conn, reply)
			}()
		}
	}()

	boundHost, boundPort, _ := net.SplitHostPort(l.Addr().String())
	number, _ := strconv.Atoi(boundPort)
	return boundHost, number
}

func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()

	_, port, _ := net.SplitHostPort(l.Addr().String())
	number, _ := strconv.Atoi(port)
	return number
}

func speak(t *testing.T, port int) string {
	t.Helper()

	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		&tls.Config{InsecureSkipVerify: true, ServerName: "balancer.test"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(body)
}

func TestATLSConnectionIsTerminatedAndForwardedInTheClear(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	host, targetPort := backend(t, "hello from a plain backend")

	m := New(slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)

	port := freePort(t)
	err := m.Apply(context.Background(), []Endpoint{{
		ID: "lb-1", ListenPort: port, TargetPort: targetPort,
		Certificate: certPEM, PrivateKey: keyPEM, Targets: []string{host},
	}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := speak(t, port); got != "hello from a plain backend" {
		t.Fatalf("got %q, want the backend's reply through the terminated connection", got)
	}
}

func TestPickWalksTheTargetsInOrderAndWraps(t *testing.T) {
	l := &listener{}
	targets := []string{"a", "b", "c"}
	l.targets.Store(&targets)

	var got []string
	for range 7 {
		target, ok := l.pick()
		if !ok {
			t.Fatal("pick refused with targets present")
		}
		got = append(got, target)
	}

	want := "a,b,c,a,b,c,a"
	if joined := strings.Join(got, ","); joined != want {
		t.Fatalf("picks = %s, want %s", joined, want)
	}
}

func TestPickRefusesWithNoTargets(t *testing.T) {
	l := &listener{}
	empty := []string{}
	l.targets.Store(&empty)

	if _, ok := l.pick(); ok {
		t.Fatal("pick returned a target from an empty list, which would dial the empty address")
	}

	bare := &listener{}
	if _, ok := bare.pick(); ok {
		t.Fatal("pick returned a target before any were stored")
	}
}

func TestConnectionsAreSpreadAcrossBackends(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	firstHost, firstPort := backendOn(t, "127.0.0.1", 0, "one")
	secondHost, _ := backendOn(t, "127.0.0.2", firstPort, "two")

	m := New(slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)

	port := freePort(t)
	if err := m.Apply(context.Background(), []Endpoint{{
		ID: "lb-1", ListenPort: port, TargetPort: firstPort,
		Certificate: certPEM, PrivateKey: keyPEM,
		Targets: []string{firstHost, secondHost},
	}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	seen := map[string]int{}
	for range 6 {
		seen[speak(t, port)]++
	}
	if len(seen) != 2 {
		t.Fatalf("replies = %v, want both backends to have taken connections", seen)
	}
}

func TestChangingBackendsKeepsTheListenerUp(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	firstHost, firstPort := backend(t, "one")

	m := New(slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)

	port := freePort(t)
	endpoint := Endpoint{
		ID: "lb-1", ListenPort: port, TargetPort: firstPort,
		Certificate: certPEM, PrivateKey: keyPEM, Targets: []string{firstHost},
	}
	if err := m.Apply(context.Background(), []Endpoint{endpoint}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	before := m.fingerprintOf("lb-1")

	endpoint.Targets = []string{firstHost, firstHost}
	if err := m.Apply(context.Background(), []Endpoint{endpoint}); err != nil {
		t.Fatalf("reapply: %v", err)
	}

	if after := m.fingerprintOf("lb-1"); after != before {
		t.Fatal("a membership change restarted the listener, which drops every live connection")
	}
	if got := speak(t, port); got != "one" {
		t.Fatalf("got %q after the membership change", got)
	}
}

func TestARemovedBalancerReleasesItsPort(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	host, targetPort := backend(t, "one")

	m := New(slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)

	port := freePort(t)
	if err := m.Apply(context.Background(), []Endpoint{{
		ID: "lb-1", ListenPort: port, TargetPort: targetPort,
		Certificate: certPEM, PrivateKey: keyPEM, Targets: []string{host},
	}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if err := m.Apply(context.Background(), nil); err != nil {
		t.Fatalf("apply nothing: %v", err)
	}

	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("the port is still held after the balancer was removed: %v", err)
	}
	l.Close()
}

func TestABackendlessBalancerRefusesRatherThanHangs(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)

	m := New(slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)

	port := freePort(t)
	if err := m.Apply(context.Background(), []Endpoint{{
		ID: "lb-1", ListenPort: port, TargetPort: 80,
		Certificate: certPEM, PrivateKey: keyPEM,
	}}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		&tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(conn); err == nil {
		return
	}
}

func TestAnIPv6BackendIsReached(t *testing.T) {
	certPEM, keyPEM := selfSigned(t)
	host, targetPort := backendOn(t, "::1", 0, "over v6 behind tls")

	m := New(slog.New(slog.DiscardHandler))
	t.Cleanup(m.Close)

	port := freePort(t)
	err := m.Apply(context.Background(), []Endpoint{{
		ID: "lb-1", ListenPort: port, TargetPort: targetPort,
		Certificate: certPEM, PrivateKey: keyPEM, Targets: []string{host},
	}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := speak(t, port); got != "over v6 behind tls" {
		t.Fatalf("got %q, want the v6 backend's reply: JoinHostPort must bracket the "+
			"address or the dial parses the colons as a port", got)
	}
}

func TestAV6AddressIsBracketedWhenDialled(t *testing.T) {
	if got := net.JoinHostPort("fd00:a::5", "80"); got != "[fd00:a::5]:80" {
		t.Fatalf("dial target = %q, want it bracketed", got)
	}
}
