package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

type certBody struct {
	ID  string `json:"id"`
	TLS *struct {
		Subject   string `json:"subject"`
		ExpiresAt string `json:"expires_at"`
	} `json:"tls"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

type certListBody struct {
	Balancers []certBody `json:"balancers"`
}

func pemPair(t *testing.T, name string) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(48 * time.Hour),
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

func attachCert(t *testing.T, a *testApp, id, certPEM, keyPEM string) *testResponse {
	t.Helper()

	body, err := json.Marshal(map[string]string{
		"certificate": certPEM,
		"private_key": keyPEM,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	rec := do(t, a, http.MethodPut, "/v1/balancers/"+id+"/certificate",
		strings.NewReader(string(body)))

	var got certBody
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return &testResponse{code: rec.Code, raw: rec.Body.String(), body: got}
}

type testResponse struct {
	code int
	raw  string
	body certBody
}

func sealingBalancer(t *testing.T) (*testApp, string, string) {
	t.Helper()

	a, _ := sealingAppIn(t, t.TempDir())
	nodeID := registerNode(t, a, "bm-1", "rack-a")

	rec := do(t, a, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"web","target_port":80,"listen_port":8443}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create balancer: %d %s", rec.Code, rec.Body.String())
	}

	var created certBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return a, nodeID, created.ID
}

func TestACertificateIsSummarisedForTheOperatorAndServedToTheNode(t *testing.T) {
	a, nodeID, id := sealingBalancer(t)
	certPEM, keyPEM := pemPair(t, "web.example")

	got := attachCert(t, a, id, certPEM, keyPEM)
	if got.code != http.StatusOK {
		t.Fatalf("attach: %d %s", got.code, got.raw)
	}
	if got.body.TLS == nil || got.body.TLS.Subject != "web.example" {
		t.Fatalf("tls = %+v, want the subject summarised", got.body.TLS)
	}
	if strings.Contains(got.raw, "PRIVATE KEY") {
		t.Fatal("the response carried the private key back")
	}

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/balancers", nil)
	var list certListBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, b := range list.Balancers {
		if b.ID != id {
			continue
		}
		if b.Certificate != certPEM || b.PrivateKey != keyPEM {
			t.Fatal("the node did not get the material it has to terminate with")
		}
		return
	}
	t.Fatal("the balancer is not in the node's view")
}

func TestAPrivateKeyIsNotOnDiskInTheClear(t *testing.T) {
	a, dir := sealingAppIn(t, t.TempDir())
	registerNode(t, a, "bm-1", "rack-a")

	rec := do(t, a, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"web","target_port":80,"listen_port":8443}`))
	var created certBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	certPEM, keyPEM := pemPair(t, "web.example")
	if got := attachCert(t, a, created.ID, certPEM, keyPEM); got.code != http.StatusOK {
		t.Fatalf("attach: %d %s", got.code, got.raw)
	}

	onDisk := everythingUnder(t, dir)
	if strings.Contains(onDisk, "PRIVATE KEY") {
		t.Fatal("a private key sits in the control plane's data directory in the clear")
	}
	if !strings.Contains(onDisk, "web.example") {
		t.Fatal("the subject is not stored, so listing balancers would need the key")
	}
}

func TestAMismatchedPairIsRefusedWhenItIsAttached(t *testing.T) {
	a, _, id := sealingBalancer(t)

	certPEM, _ := pemPair(t, "web.example")
	_, otherKey := pemPair(t, "other.example")

	got := attachCert(t, a, id, certPEM, otherKey)
	if got.code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a key that does not match its certificate must fail "+
			"here, not at 3am when a connection arrives", got.code, http.StatusBadRequest)
	}
}

func TestGarbageIsRefused(t *testing.T) {
	a, _, id := sealingBalancer(t)

	certPEM, keyPEM := pemPair(t, "web.example")
	for _, pair := range [][2]string{
		{"not a certificate", keyPEM},
		{certPEM, "not a key"},
		{"", keyPEM},
	} {
		if got := attachCert(t, a, id, pair[0], pair[1]); got.code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", got.code, http.StatusBadRequest)
		}
	}
}

func TestACertificateIsRefusedWithNoKeyToSealItWith(t *testing.T) {
	a, _ := newBalancingApp(t)

	rec := do(t, a, http.MethodPost, "/v1/balancers",
		strings.NewReader(`{"name":"web","target_port":80,"listen_port":8443}`))
	var created certBody
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	certPEM, keyPEM := pemPair(t, "web.example")
	got := attachCert(t, a, created.ID, certPEM, keyPEM)

	if got.code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: handing a private key to every node from a database "+
			"it cannot protect must be refused", got.code, http.StatusConflict)
	}
}

func TestRemovingACertificateTakesTheBalancerBackToPlain(t *testing.T) {
	a, nodeID, id := sealingBalancer(t)
	certPEM, keyPEM := pemPair(t, "web.example")
	attachCert(t, a, id, certPEM, keyPEM)

	rec := do(t, a, http.MethodDelete, "/v1/balancers/"+id+"/certificate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}

	var got certBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TLS != nil {
		t.Fatalf("tls = %+v, want nothing left", got.TLS)
	}

	nodeRec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/balancers", nil)
	if strings.Contains(nodeRec.Body.String(), "PRIVATE KEY") {
		t.Fatal("the node still gets a key for a balancer that no longer terminates")
	}
}
