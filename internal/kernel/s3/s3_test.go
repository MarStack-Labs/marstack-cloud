package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()

	c, err := New(Config{
		Endpoint: endpoint, Bucket: "backups", Region: "us-east-1",
		AccessKey: "minioadmin", SecretKey: "minioadmin",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	c.now = func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC) }
	return c
}

func TestAConfigMustNameEverythingItNeeds(t *testing.T) {
	full := Config{
		Endpoint: "http://minio:9000", Bucket: "b", AccessKey: "a", SecretKey: "s",
	}

	missing := map[string]Config{
		"no endpoint":   {Bucket: "b", AccessKey: "a", SecretKey: "s"},
		"no bucket":     {Endpoint: "http://minio:9000", AccessKey: "a", SecretKey: "s"},
		"no access key": {Endpoint: "http://minio:9000", Bucket: "b", SecretKey: "s"},
		"no secret key": {Endpoint: "http://minio:9000", Bucket: "b", AccessKey: "a"},
	}

	for what, cfg := range missing {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}

	if _, err := New(full); err != nil {
		t.Fatalf("a complete config was refused: %v", err)
	}
}

func TestAnEndpointWithoutASchemeGetsOne(t *testing.T) {
	c, err := New(Config{
		Endpoint: "minio.internal:9000", Bucket: "b", AccessKey: "a", SecretKey: "s",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !strings.HasPrefix(c.Endpoint(), "http://") {
		t.Fatalf("endpoint = %q, want a scheme filled in", c.Endpoint())
	}
}

func TestASignedRequestCarriesWhatS3Checks(t *testing.T) {
	var got *http.Request

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	if err := c.Put(context.Background(), "backups/bkp-1",
		strings.NewReader("bytes"), 5); err != nil {
		t.Fatalf("put: %v", err)
	}

	auth := got.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256",
		"Credential=minioadmin/20260829/us-east-1/s3/aws4_request",
		"SignedHeaders=",
		"Signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("Authorization is missing %q:\n%s", want, auth)
		}
	}

	if got.Header.Get("X-Amz-Date") != "20260829T120000Z" {
		t.Errorf("X-Amz-Date = %q", got.Header.Get("X-Amz-Date"))
	}
	if got.Header.Get("X-Amz-Content-Sha256") != unsignedPayload {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %q",
			got.Header.Get("X-Amz-Content-Sha256"), unsignedPayload)
	}
	if !strings.Contains(auth, "host") {
		t.Errorf("SignedHeaders does not cover host, which S3 requires:\n%s", auth)
	}
}

func TestTheSignatureChangesWithTheRequest(t *testing.T) {
	c := newTestClient(t, "http://minio:9000")

	signatureOf := func(method, key string) string {
		req, err := http.NewRequest(method, c.urlFor(key), nil)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		c.sign(req, c.now())
		return req.Header.Get("Authorization")
	}

	base := signatureOf(http.MethodGet, "a")
	if base == signatureOf(http.MethodGet, "b") {
		t.Fatal("two different keys signed identically")
	}
	if base == signatureOf(http.MethodDelete, "a") {
		t.Fatal("two different methods signed identically")
	}
	if base != signatureOf(http.MethodGet, "a") {
		t.Fatal("the same request signed differently twice")
	}
}

func TestAMissingObjectIsNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	ctx := context.Background()

	if _, err := c.Get(ctx, "gone"); err != ErrNotFound {
		t.Fatalf("get = %v, want ErrNotFound so a caller can tell it apart", err)
	}
	if err := c.Delete(ctx, "gone"); err != nil {
		t.Fatalf("delete of a missing object = %v, want it to be a no-op", err)
	}
	if err := c.Check(ctx); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("check = %v, want it to say the bucket is missing", err)
	}
}

func TestAnS3ErrorIsReportedInWords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `<?xml version="1.0"?><Error><Code>SignatureDoesNotMatch</Code>`+
			`<Message>The request signature we calculated does not match</Message></Error>`)
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	err := c.Put(context.Background(), "k", strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("a 403 was treated as success")
	}
	if !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("error = %q, want the code S3 gave rather than raw xml", err)
	}
}

func TestARoundTrip(t *testing.T) {
	held := map[string][]byte{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			held[r.URL.Path] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			body, there := held[r.URL.Path]
			if !there {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(body)
		case http.MethodDelete:
			delete(held, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server.URL)
	ctx := context.Background()

	if err := c.Put(ctx, "backups/bkp-1", strings.NewReader("hello"), 5); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, there := held["/backups/backups/bkp-1"]; !there {
		t.Fatalf("stored under %v, want the bucket in the path", keysOf(held))
	}

	body, err := c.Get(ctx, "backups/bkp-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	back, _ := io.ReadAll(body)
	body.Close()
	if string(back) != "hello" {
		t.Fatalf("got %q, want hello", back)
	}

	if err := c.Delete(ctx, "backups/bkp-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Get(ctx, "backups/bkp-1"); err != ErrNotFound {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

func keysOf(held map[string][]byte) []string {
	out := make([]string, 0, len(held))
	for key := range held {
		out = append(out, key)
	}
	return out
}
