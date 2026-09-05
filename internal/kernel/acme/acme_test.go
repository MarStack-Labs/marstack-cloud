package acme

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
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCA struct {
	mu sync.Mutex

	base       string
	nonces     int
	accounts   int
	orderState string
	accepted   []string
	seenNonce  map[string]bool
	lastJWS    map[string]any
}

func newFakeCA(t *testing.T) *fakeCA {
	t.Helper()

	ca := &fakeCA{orderState: StatusPending, seenNonce: map[string]bool{}}
	srv := httptest.NewServer(ca.handler(t))
	t.Cleanup(srv.Close)
	ca.base = srv.URL
	return ca
}

func (ca *fakeCA) handler(t *testing.T) http.Handler {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /directory", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"newNonce":   ca.base + "/nonce",
			"newAccount": ca.base + "/account",
			"newOrder":   ca.base + "/order",
		})
	})

	mux.HandleFunc("/nonce", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Replay-Nonce", ca.mintNonce())
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /account", func(w http.ResponseWriter, r *http.Request) {
		ca.readJWS(t, r)
		ca.mu.Lock()
		ca.accounts++
		ca.mu.Unlock()

		w.Header().Set("Replay-Nonce", ca.mintNonce())
		w.Header().Set("Location", ca.base+"/account/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"valid"}`))
	})

	mux.HandleFunc("POST /order", func(w http.ResponseWriter, r *http.Request) {
		ca.readJWS(t, r)
		w.Header().Set("Replay-Nonce", ca.mintNonce())
		w.Header().Set("Location", ca.base+"/order/1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"pending","finalize":"` + ca.base + `/finalize",
			"authorizations":["` + ca.base + `/authz/1"]}`))
	})

	mux.HandleFunc("POST /authz/1", func(w http.ResponseWriter, r *http.Request) {
		ca.readJWS(t, r)
		w.Header().Set("Replay-Nonce", ca.mintNonce())
		_, _ = w.Write([]byte(`{"identifier":{"type":"dns","value":"app.test"},
			"challenges":[
				{"type":"dns-01","url":"` + ca.base + `/challenge/dns","token":"wrong"},
				{"type":"http-01","url":"` + ca.base + `/challenge/http","token":"tok3n"}]}`))
	})

	mux.HandleFunc("POST /challenge/http", func(w http.ResponseWriter, r *http.Request) {
		ca.readJWS(t, r)
		ca.mu.Lock()
		ca.accepted = append(ca.accepted, "http-01")
		ca.orderState = StatusValid
		ca.mu.Unlock()

		w.Header().Set("Replay-Nonce", ca.mintNonce())
		_, _ = w.Write([]byte(`{"status":"processing"}`))
	})

	mux.HandleFunc("POST /order/1", func(w http.ResponseWriter, r *http.Request) {
		ca.readJWS(t, r)
		ca.mu.Lock()
		state := ca.orderState
		ca.mu.Unlock()

		w.Header().Set("Replay-Nonce", ca.mintNonce())
		body := `{"status":"` + state + `","finalize":"` + ca.base + `/finalize"`
		if state == StatusValid {
			body += `,"certificate":"` + ca.base + `/cert"`
		}
		_, _ = w.Write([]byte(body + `}`))
	})

	mux.HandleFunc("POST /finalize", func(w http.ResponseWriter, r *http.Request) {
		ca.readJWS(t, r)
		w.Header().Set("Replay-Nonce", ca.mintNonce())
		_, _ = w.Write([]byte(`{"status":"valid","certificate":"` + ca.base + `/cert"}`))
	})

	mux.HandleFunc("POST /cert", func(w http.ResponseWriter, r *http.Request) {
		ca.readJWS(t, r)
		w.Header().Set("Replay-Nonce", ca.mintNonce())
		_, _ = w.Write([]byte(issuedCertificate(t)))
	})

	return mux
}

func (ca *fakeCA) mintNonce() string {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	ca.nonces++
	return "nonce-" + string(rune('a'+ca.nonces%26)) + string(rune('0'+ca.nonces%10))
}

func (ca *fakeCA) readJWS(t *testing.T, r *http.Request) {
	t.Helper()

	if got := r.Header.Get("Content-Type"); got != "application/jose+json" {
		t.Errorf("content type = %q, want application/jose+json", got)
	}

	var envelope struct {
		Protected string `json:"protected"`
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode the jws: %v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(envelope.Protected)
	if err != nil {
		t.Fatalf("decode the header: %v", err)
	}

	var header map[string]any
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatalf("decode the header json: %v", err)
	}

	if header["alg"] != "ES256" {
		t.Errorf("alg = %v, want ES256", header["alg"])
	}
	if header["url"] != ca.base+r.URL.Path {
		t.Errorf("url = %v, want %s", header["url"], ca.base+r.URL.Path)
	}

	nonce, _ := header["nonce"].(string)
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if nonce == "" {
		t.Error("the request carried no nonce")
	}
	if ca.seenNonce[nonce] {
		t.Errorf("nonce %q was used twice, which a real directory refuses", nonce)
	}
	ca.seenNonce[nonce] = true
	ca.lastJWS = header
}

func issuedCertificate(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "app.test"},
		DNSNames:     []string{"app.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestAWholeOrderRunsThrough(t *testing.T) {
	ca := newFakeCA(t)
	client := New(ca.base+"/directory", nil)

	account, err := client.Register(context.Background(), "ops@marstack.test")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if account.Key == "" || account.URL == "" {
		t.Fatalf("account = %+v, want a key and a url to keep", account)
	}

	order, err := client.Order(context.Background(), []string{"app.test"})
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if len(order.Challenges) != 1 {
		t.Fatalf("challenges = %+v, want the http-01 one", order.Challenges)
	}
	if order.Challenges[0].Token != "tok3n" {
		t.Fatalf("token = %q, want the http-01 token rather than the dns-01 one",
			order.Challenges[0].Token)
	}

	authorization, err := client.KeyAuthorization("tok3n")
	if err != nil {
		t.Fatalf("key authorization: %v", err)
	}
	if !strings.HasPrefix(authorization, "tok3n.") || len(authorization) < 20 {
		t.Fatalf("key authorization = %q, want the token and a thumbprint", authorization)
	}

	if err := client.Accept(context.Background(), order.Challenges[0].URL); err != nil {
		t.Fatalf("accept: %v", err)
	}

	key, orderURL, err := client.Finalize(context.Background(), order, []string{"app.test"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if !strings.Contains(key, "EC PRIVATE KEY") {
		t.Fatalf("key = %q, want a PEM private key", key)
	}

	settled, err := client.Poll(context.Background(), orderURL, time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if settled.Status != StatusValid {
		t.Fatalf("status = %q, want valid", settled.Status)
	}

	chain, err := client.Download(context.Background(), settled.Certificate)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !strings.Contains(chain, "BEGIN CERTIFICATE") {
		t.Fatalf("chain = %q, want PEM", chain)
	}
}

func TestTheFirstRequestCarriesTheKeyAndTheRestCarryTheAccount(t *testing.T) {
	ca := newFakeCA(t)
	client := New(ca.base+"/directory", nil)

	if _, err := client.Register(context.Background(), ""); err != nil {
		t.Fatalf("register: %v", err)
	}

	ca.mu.Lock()
	first := ca.lastJWS
	ca.mu.Unlock()
	if _, held := first["jwk"]; !held {
		t.Fatal("the account request carried no jwk, and a directory that has never seen " +
			"this key has no other way to learn it")
	}

	if _, err := client.Order(context.Background(), []string{"app.test"}); err != nil {
		t.Fatalf("order: %v", err)
	}

	ca.mu.Lock()
	later := ca.lastJWS
	ca.mu.Unlock()
	if _, held := later["jwk"]; held {
		t.Fatal("a later request still carried the jwk. Once an account exists the directory " +
			"expects its url as kid, and sending both is refused")
	}
	if later["kid"] == nil {
		t.Fatal("a later request carried no kid")
	}
}

func TestAnAccountKeyCanBeKeptAndUsedAgain(t *testing.T) {
	ca := newFakeCA(t)

	first := New(ca.base+"/directory", nil)
	account, err := first.Register(context.Background(), "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	second := New(ca.base+"/directory", nil)
	if err := second.UseAccount(account); err != nil {
		t.Fatalf("reuse: %v", err)
	}

	if _, err := second.Order(context.Background(), []string{"app.test"}); err != nil {
		t.Fatalf("order with the kept account: %v", err)
	}

	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.accounts != 1 {
		t.Fatalf("the directory registered %d accounts, want one: a new account per renewal "+
			"is how a rate limit is reached", ca.accounts)
	}
}

func TestAProblemDocumentBecomesTheError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/directory" {
			_, _ = w.Write([]byte(`{"newNonce":"` + "http://" + r.Host + `/nonce",
				"newAccount":"` + "http://" + r.Host + `/account",
				"newOrder":"` + "http://" + r.Host + `/order"}`))
			return
		}
		if r.URL.Path == "/nonce" {
			w.Header().Set("Replay-Nonce", "n1")
			return
		}
		w.Header().Set("Replay-Nonce", "n2")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:rateLimited",
			"detail":"too many certificates already issued for app.test"}`))
	}))
	defer srv.Close()

	client := New(srv.URL+"/directory", nil)
	_, err := client.Register(context.Background(), "")

	if err == nil {
		t.Fatal("a refusal was treated as success")
	}
	if !strings.Contains(err.Error(), "too many certificates") {
		t.Fatalf("error = %v, want the detail the directory wrote. A status code alone "+
			"leaves an operator guessing why nothing was issued", err)
	}
	if strings.Contains(err.Error(), "urn:ietf") || strings.Contains(err.Error(), `"detail"`) {
		t.Fatalf("error = %v, want the sentence on its own rather than the whole problem "+
			"document: an operator reads this, and json is not a message", err)
	}
}
