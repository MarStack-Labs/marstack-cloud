package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func pemPairLasting(t *testing.T, name string, life time.Duration) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-2 * time.Hour),
		NotAfter:     time.Now().Add(life),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	blob, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: blob}))
}

var nextExpiryPort = 9100

func balancerWithCertificate(t *testing.T, a *testApp, name string, life time.Duration) string {
	t.Helper()

	nextExpiryPort++
	created := createBalancer(t, a, a.secret, `{"name":"`+name+`","target_port":80,
		"listen_port":`+strconv.Itoa(nextExpiryPort)+`}`)

	certPEM, keyPEM := pemPairLasting(t, name+".example", life)
	if got := attachCert(t, a, created.ID, certPEM, keyPEM); got.code != http.StatusOK {
		t.Fatalf("attach: %d %s", got.code, got.raw)
	}
	return created.ID
}

func TestACertificateAboutToExpireIsReportedOnce(t *testing.T) {
	a, _ := newSealingApp(t)
	id := balancerWithCertificate(t, a, "soon", 3*24*time.Hour+12*time.Hour)

	for range 3 {
		a.balancers.Sweep(context.Background())
	}

	entries := readEvents(t, a, a.secret, "?subject="+id+"&kind=certificate.expiring")
	if len(entries) != 1 {
		t.Fatalf("events = %d, want exactly one: a sweep that reports a condition rather "+
			"than a transition writes one every ten minutes forever", len(entries))
	}
	if !strings.Contains(entries[0].Message, "soon.example") {
		t.Fatalf("message = %q, want the name the certificate is for", entries[0].Message)
	}
	if !strings.Contains(entries[0].Message, "3 days") {
		t.Fatalf("message = %q, want how long is left. The remaining time is floored, never "+
			"rounded up: a warning that claims more time than there is, is worse than none",
			entries[0].Message)
	}
}

func TestACertificateWithPlentyOfLifeIsNotReported(t *testing.T) {
	a, _ := newSealingApp(t)
	id := balancerWithCertificate(t, a, "fine", 90*24*time.Hour)

	a.balancers.Sweep(context.Background())

	if entries := readEvents(t, a, a.secret, "?subject="+id); len(entries) != 0 {
		t.Fatalf("events = %+v, want none: warning about a certificate with three months "+
			"left is how a warning stops being read", entries)
	}
}

func TestAnExpiredCertificateIsReportedAsAnError(t *testing.T) {
	a, _ := newSealingApp(t)
	id := balancerWithCertificate(t, a, "gone", -time.Hour)

	a.balancers.Sweep(context.Background())

	entries := readEvents(t, a, a.secret, "?subject="+id+"&kind=certificate.expired")
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want the expiry reported", entries)
	}
	if entries[0].Severity != "error" {
		t.Fatalf("severity = %q, want error: a port that answers nothing a client trusts is "+
			"not a warning", entries[0].Severity)
	}
	if !strings.Contains(entries[0].Message, "expired") {
		t.Fatalf("message = %q", entries[0].Message)
	}
}

func TestCrossingFromExpiringToExpiredIsReportedAgain(t *testing.T) {
	a, _ := newSealingApp(t)
	id := balancerWithCertificate(t, a, "crossing", time.Hour)

	a.balancers.Sweep(context.Background())
	if entries := readEvents(t, a, a.secret,
		"?subject="+id+"&kind=certificate.expiring"); len(entries) != 1 {
		t.Fatalf("expiring events = %+v, want one", entries)
	}

	certPEM, keyPEM := pemPairLasting(t, "crossing.example", -time.Minute)
	if got := attachCert(t, a, id, certPEM, keyPEM); got.code != http.StatusOK {
		t.Fatalf("replace: %d %s", got.code, got.raw)
	}
	a.balancers.Sweep(context.Background())

	if entries := readEvents(t, a, a.secret,
		"?subject="+id+"&kind=certificate.expired"); len(entries) != 1 {
		t.Fatalf("expired events = %+v, want the crossing reported: a certificate that was "+
			"already warned about must still say when it actually died", entries)
	}
}

func TestAReplacedCertificateGoesQuiet(t *testing.T) {
	a, _ := newSealingApp(t)
	id := balancerWithCertificate(t, a, "renewed", 2*24*time.Hour)

	a.balancers.Sweep(context.Background())

	certPEM, keyPEM := pemPairLasting(t, "renewed.example", 120*24*time.Hour)
	if got := attachCert(t, a, id, certPEM, keyPEM); got.code != http.StatusOK {
		t.Fatalf("renew: %d %s", got.code, got.raw)
	}

	before := len(readEvents(t, a, a.secret, "?subject="+id))
	for range 3 {
		a.balancers.Sweep(context.Background())
	}

	if after := len(readEvents(t, a, a.secret, "?subject="+id)); after != before {
		t.Fatalf("events went from %d to %d after a renewal, want silence", before, after)
	}

	short, shortKey := pemPairLasting(t, "renewed.example", 2*24*time.Hour)
	if got := attachCert(t, a, id, short, shortKey); got.code != http.StatusOK {
		t.Fatalf("second renewal: %d %s", got.code, got.raw)
	}
	a.balancers.Sweep(context.Background())

	if entries := readEvents(t, a, a.secret,
		"?subject="+id+"&kind=certificate.expiring"); len(entries) != 2 {
		t.Fatalf("expiring events = %d, want two: a certificate that went back to healthy "+
			"and then approached expiry again must warn again, so the remembered state has "+
			"to be cleared while it is healthy", len(entries))
	}
}

func TestAPublishedPortCertificateIsWatchedToo(t *testing.T) {
	a, nodeID := newSealingApp(t)

	id := createVMOnNetworks(t, a, "web", newNetwork(t, a, "pub", "10.210.0.0/16"))
	waitForNICs(t, a, nodeID, id, 1)

	created := createIn(t, a, a.secret, "/v1/forwards",
		`{"instance_id":"`+id+`","node_port":9443,"target_port":443}`)

	certPEM, keyPEM := pemPairLasting(t, "port.example", 24*time.Hour)
	body, err := json.Marshal(map[string]string{
		"certificate": certPEM, "private_key": keyPEM,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	rec := doAs(t, a, a.secret, http.MethodPut, "/v1/forwards/"+created+"/certificate",
		strings.NewReader(string(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
	}

	a.forwards.Sweep(context.Background())

	entries := readEvents(t, a, a.secret, "?subject="+created+"&kind=certificate.expiring")
	if len(entries) != 1 {
		t.Fatalf("events = %+v, want a published port watched the same as a balancer: it "+
			"terminates TLS the same way", entries)
	}
	if !strings.Contains(entries[0].Message, "9443") {
		t.Fatalf("message = %q, want the port named", entries[0].Message)
	}
}
