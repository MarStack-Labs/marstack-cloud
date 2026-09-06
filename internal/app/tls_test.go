package app

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
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/certs"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

func selfSigned(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "marstack-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}

	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func freeAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func serveTLS(t *testing.T) (address, certPath string) {
	t.Helper()

	dir := schemaDir(t)
	certPath, keyPath := selfSigned(t, dir)
	address = freeAddress(t)

	a, err := New(context.Background(), Config{
		Listen:        address,
		DataDir:       dir,
		TLSCert:       certPath,
		TLSKey:        keyPath,
		RatePerSecond: &unlimited,
	}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		stop()
		<-done
		a.Close()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return address, certPath
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the control plane never started listening")
	return "", ""
}

func TestTheControlPlaneServesHTTPS(t *testing.T) {
	address, certPath := serveTLS(t)

	trusted, err := certs.Client(certPath)
	if err != nil {
		t.Fatalf("load the certificate authority: %v", err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = trusted
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	res, err := client.Get("https://" + address + "/healthz")
	if err != nil {
		t.Fatalf("call over https: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if res.TLS == nil {
		t.Fatal("the answer came back without TLS")
	}
	if res.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("negotiated %x, want TLS 1.2 or better", res.TLS.Version)
	}
}

func TestAnUntrustedClientIsTurnedAway(t *testing.T) {
	address, _ := serveTLS(t)

	client := &http.Client{Timeout: 5 * time.Second}
	if _, err := client.Get("https://" + address + "/healthz"); err == nil {
		t.Fatal("a client that trusts nothing still connected, so the certificate is not " +
			"being verified and the channel proves nothing")
	}
}

func TestPlainHTTPIsRefusedByAnHTTPSListener(t *testing.T) {
	address, _ := serveTLS(t)

	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get("http://" + address + "/healthz")
	if err != nil {
		return
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a cleartext request must not be served",
			res.StatusCode, http.StatusBadRequest)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<12))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "HTTPS") {
		t.Fatalf("body = %q, want an answer that says what went wrong", body)
	}
}

func TestABrokenCertificatePairIsRefusedAtStartup(t *testing.T) {
	dir := schemaDir(t)
	certPath, _ := selfSigned(t, dir)

	wrongKey := filepath.Join(dir, "other.pem")
	if err := os.WriteFile(wrongKey, []byte("not a key"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := New(context.Background(), Config{
		DataDir: dir,
		TLSCert: certPath,
		TLSKey:  wrongKey,
	}, logging.New("error", io.Discard))
	if err == nil {
		t.Fatal("the control plane started with a certificate it cannot serve, and would only " +
			"have found out when the first client called")
	}
}
