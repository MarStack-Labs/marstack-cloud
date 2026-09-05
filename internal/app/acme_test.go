package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

type stubCA struct {
	mu sync.Mutex

	base     string
	state    string
	orders   int
	accepted int
	csr      []byte
}

func readCSR(t *testing.T, r *http.Request) []byte {
	t.Helper()

	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode the jws: %v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatalf("decode the payload: %v", err)
	}

	var body struct {
		CSR string `json:"csr"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode the finalize body: %v", err)
	}

	der, err := base64.RawURLEncoding.DecodeString(body.CSR)
	if err != nil {
		t.Fatalf("decode the csr: %v", err)
	}
	return der
}

func (ca *stubCA) issue(t *testing.T, der []byte) string {
	t.Helper()

	if len(der) == 0 {
		t.Fatal("the authority was asked for a certificate before any request was finalized")
	}

	asked, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatalf("parse the csr: %v", err)
	}
	if err := asked.CheckSignature(); err != nil {
		t.Fatalf("the csr is not signed by the key it carries: %v", err)
	}

	authority, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("authority key: %v", err)
	}

	root := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "stub authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, &root, &root,
		&authority.PublicKey, authority)
	if err != nil {
		t.Fatalf("authority certificate: %v", err)
	}
	parsedRoot, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatalf("parse the authority certificate: %v", err)
	}

	leaf := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      asked.Subject,
		DNSNames:     asked.DNSNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leaf, parsedRoot,
		asked.PublicKey, authority)
	if err != nil {
		t.Fatalf("leaf certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) +
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}))
}

func newStubCA(t *testing.T) *stubCA {
	t.Helper()

	ca := &stubCA{state: "pending"}
	mux := http.NewServeMux()

	mux.HandleFunc("/directory", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"newNonce":   ca.base + "/nonce",
			"newAccount": ca.base + "/account",
			"newOrder":   ca.base + "/order",
		})
	})
	mux.HandleFunc("/nonce", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Replay-Nonce", ca.mint())
	})
	mux.HandleFunc("/account", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Replay-Nonce", ca.mint())
		w.Header().Set("Location", ca.base+"/account/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"valid"}`))
	})
	mux.HandleFunc("/order", func(w http.ResponseWriter, _ *http.Request) {
		ca.mu.Lock()
		ca.orders++
		ca.mu.Unlock()

		w.Header().Set("Replay-Nonce", ca.mint())
		w.Header().Set("Location", ca.base+"/order/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"pending","finalize":"` + ca.base +
			`/finalize","authorizations":["` + ca.base + `/authz/1"]}`))
	})
	mux.HandleFunc("/authz/1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Replay-Nonce", ca.mint())
		_, _ = w.Write([]byte(`{"identifier":{"type":"dns","value":"app.test"},
			"challenges":[{"type":"http-01","url":"` + ca.base +
			`/challenge","token":"tok-live"}]}`))
	})
	mux.HandleFunc("/challenge", func(w http.ResponseWriter, _ *http.Request) {
		ca.mu.Lock()
		ca.accepted++
		ca.state = "ready"
		ca.mu.Unlock()

		w.Header().Set("Replay-Nonce", ca.mint())
		_, _ = w.Write([]byte(`{"status":"processing"}`))
	})
	mux.HandleFunc("/order/1", func(w http.ResponseWriter, _ *http.Request) {
		ca.mu.Lock()
		state := ca.state
		ca.mu.Unlock()

		w.Header().Set("Replay-Nonce", ca.mint())
		body := `{"status":"` + state + `","finalize":"` + ca.base + `/finalize"`
		if state == "valid" {
			body += `,"certificate":"` + ca.base + `/cert"`
		}
		_, _ = w.Write([]byte(body + `}`))
	})
	mux.HandleFunc("/finalize", func(w http.ResponseWriter, r *http.Request) {
		ca.mu.Lock()
		ca.state = "valid"
		ca.csr = readCSR(t, r)
		ca.mu.Unlock()

		w.Header().Set("Replay-Nonce", ca.mint())
		_, _ = w.Write([]byte(`{"status":"valid","certificate":"` + ca.base + `/cert"}`))
	})
	mux.HandleFunc("/cert", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Replay-Nonce", ca.mint())

		ca.mu.Lock()
		asked := ca.csr
		ca.mu.Unlock()

		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write([]byte(ca.issue(t, asked)))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ca.base = srv.URL
	return ca
}

func (ca *stubCA) mint() string {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	ca.orders++
	return "n" + strings.Repeat("x", ca.orders%5+1)
}

func newACMEApp(t *testing.T, directory string) (*testApp, string, *movingClock) {
	t.Helper()

	tick := &movingClock{at: time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	k, _ := sealed.NewKey()
	built, err := New(context.Background(),
		Config{
			DataDir:           dir,
			BackupKeys:        []sealed.Key{k},
			SchedulerInterval: 10 * time.Millisecond,
			RatePerSecond:     &unlimited,
			ACMEDirectory:     directory,
			ACMEContact:       "ops@marstack.test",
			Now:               tick.now,
		}, logging.New("error", io.Discard))
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { built.Close() })

	raw, err := os.ReadFile(filepath.Join(dir, token.BootstrapFileName))
	if err != nil {
		t.Fatalf("read the bootstrap token: %v", err)
	}

	a := &testApp{App: built, secret: strings.TrimSpace(string(raw))}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.scheduler.Run(ctx)

	return a, registerNode(t, a, "bm-1", "rack-a"), tick
}

type movingClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *movingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *movingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type acmeStatusBody struct {
	BalancerID string   `json:"balancer_id"`
	Names      []string `json:"names"`
	State      string   `json:"state"`
	Message    string   `json:"message"`
	ExpiresAt  string   `json:"expires_at"`
}

func acmeStatus(t *testing.T, a *testApp, id string) acmeStatusBody {
	t.Helper()

	rec := doAs(t, a, a.secret, http.MethodGet,
		"/v1/balancers/"+id+"/certificate/acme", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}

	var held acmeStatusBody
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return held
}

func routedBalancerFor(t *testing.T, a *testApp, nodeID, host string) string {
	t.Helper()

	runningService(t, a, nodeID, "web", 1)
	created := createBalancer(t, a, a.secret, `{"name":"edge","target_port":80,
		"listen_port":80,"routes":[{"host":"`+host+`","service":"web"}]}`)
	return created.ID
}

func TestACertificateIsIssuedAndAttachedOnItsOwn(t *testing.T) {
	ca := newStubCA(t)
	a, nodeID, tick := newACMEApp(t, ca.base+"/directory")
	id := routedBalancerFor(t, a, nodeID, "app.test")

	rec := doAs(t, a, a.secret, http.MethodPost,
		"/v1/balancers/"+id+"/certificate/acme", strings.NewReader(`{}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}

	held := acmeStatus(t, a, id)
	if len(held.Names) != 1 || held.Names[0] != "app.test" {
		t.Fatalf("names = %v, want the host the balancer routes: naming them by hand for a "+
			"balancer that already says which hosts it serves is work nobody should redo",
			held.Names)
	}

	for range 6 {
		a.certificates.Sweep(context.Background())
		tick.advance(time.Minute)
	}

	held = acmeStatus(t, a, id)
	if held.State != "issued" {
		t.Fatalf("state = %q (%s), want issued", held.State, held.Message)
	}
	if held.ExpiresAt == "" {
		t.Fatal("nothing recorded when the certificate expires, so nothing can renew it")
	}

	balancers := nodeBalancers(t, a, nodeID)
	for _, b := range balancers {
		if b.ID != id {
			continue
		}
		if b.Certificate == "" || b.PrivateKey == "" {
			t.Fatal("the node was not given the certificate, so the port still answers plain " +
				"http and the whole order changed nothing")
		}
		return
	}
	t.Fatal("the balancer left the node view")
}

func TestTheChallengeReachesTheNode(t *testing.T) {
	ca := newStubCA(t)
	a, nodeID, tick := newACMEApp(t, ca.base+"/directory")
	id := routedBalancerFor(t, a, nodeID, "app.test")

	doAs(t, a, a.secret, http.MethodPost,
		"/v1/balancers/"+id+"/certificate/acme", strings.NewReader(`{}`))
	a.certificates.Sweep(context.Background())
	_ = tick

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/acme-challenges", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenges: %d %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Challenges []struct {
			Token         string `json:"token"`
			Authorization string `json:"authorization"`
		} `json:"challenges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body.Challenges) != 1 {
		t.Fatalf("challenges = %+v, want the one the authority asked for: the node answers "+
			"nothing it was not told about, and the order never completes", body.Challenges)
	}
	if body.Challenges[0].Token != "tok-live" {
		t.Fatalf("token = %q, want the authority's", body.Challenges[0].Token)
	}
	if !strings.HasPrefix(body.Challenges[0].Authorization, "tok-live.") {
		t.Fatalf("authorization = %q, want the token and the account thumbprint",
			body.Challenges[0].Authorization)
	}
}

func TestAnOrderIsRefusedWithNoAuthorityConfigured(t *testing.T) {
	a, nodeID := newBalancingApp(t)
	id := routedBalancerFor(t, a, nodeID, "app.test")

	rec := doAs(t, a, a.secret, http.MethodPost,
		"/v1/balancers/"+id+"/certificate/acme", strings.NewReader(`{}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: accepting an order nothing will ever work through "+
			"leaves a balancer waiting forever", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "acme-directory") {
		t.Fatalf("refusal = %s, want it to name the flag", rec.Body.String())
	}
}

func TestABalancerWithNoHostCannotAskForOne(t *testing.T) {
	ca := newStubCA(t)
	a, nodeID, _ := newACMEApp(t, ca.base+"/directory")

	runningInstance(t, a, a.secret, "web-1", nodeID)
	created := createBalancer(t, a, a.secret, `{"name":"plain","target_port":80,
		"listen_port":8081}`)

	rec := doAs(t, a, a.secret, http.MethodPost,
		"/v1/balancers/"+created.ID+"/certificate/acme", strings.NewReader(`{}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a certificate is for a name, and this balancer has "+
			"none", rec.Code, http.StatusBadRequest)
	}
}

func TestStoppingRenewalLeavesTheCertificateAlone(t *testing.T) {
	ca := newStubCA(t)
	a, nodeID, tick := newACMEApp(t, ca.base+"/directory")
	id := routedBalancerFor(t, a, nodeID, "app.test")

	doAs(t, a, a.secret, http.MethodPost,
		"/v1/balancers/"+id+"/certificate/acme", strings.NewReader(`{}`))
	for range 6 {
		a.certificates.Sweep(context.Background())
		tick.advance(time.Minute)
	}

	if rec := doAs(t, a, a.secret, http.MethodDelete,
		"/v1/balancers/"+id+"/certificate/acme", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
	}

	for _, b := range nodeBalancers(t, a, nodeID) {
		if b.ID == id && b.Certificate == "" {
			t.Fatal("stopping the renewal took the certificate away. The port would stop " +
				"answering https immediately, which is not what stopping a renewal means")
		}
	}
}

func TestACertificateIsRenewedBeforeItExpires(t *testing.T) {
	ca := newStubCA(t)
	a, nodeID, tick := newACMEApp(t, ca.base+"/directory")
	id := routedBalancerFor(t, a, nodeID, "app.test")

	doAs(t, a, a.secret, http.MethodPost,
		"/v1/balancers/"+id+"/certificate/acme", strings.NewReader(`{}`))
	for range 6 {
		a.certificates.Sweep(context.Background())
		tick.advance(time.Minute)
	}
	if held := acmeStatus(t, a, id); held.State != "issued" {
		t.Fatalf("state = %q (%s), want issued first", held.State, held.Message)
	}

	ca.mu.Lock()
	before := ca.orders
	ca.state = "pending"
	ca.mu.Unlock()

	a.certificates.Sweep(context.Background())
	if held := acmeStatus(t, a, id); held.State != "issued" {
		t.Fatalf("state = %q, want it left alone while there is plenty of life", held.State)
	}

	tick.advance(70 * 24 * time.Hour)
	a.certificates.Sweep(context.Background())

	if held := acmeStatus(t, a, id); held.State != "pending" {
		t.Fatalf("state = %q, want a renewal started: a certificate that expires while "+
			"nothing renews it is the outage this exists to stop", held.State)
	}

	a.certificates.Sweep(context.Background())

	rec := do(t, a, http.MethodGet, "/v1/nodes/"+nodeID+"/acme-challenges", nil)
	var body struct {
		Challenges []struct {
			Token string `json:"token"`
		} `json:"challenges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Challenges) == 0 {
		t.Fatal("the renewal placed no order, so nothing is being answered and the " +
			"certificate still runs out")
	}

	ca.mu.Lock()
	after := ca.orders
	ca.mu.Unlock()
	if after <= before {
		t.Fatal("the authority was never called again")
	}
}
