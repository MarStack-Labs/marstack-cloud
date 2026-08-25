package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

type payload struct {
	Name string `json:"name"`
}

func decodeRequest(t *testing.T, contentType, body string) (payload, error) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return Decode[payload](httptest.NewRecorder(), req)
}

func assertInvalid(t *testing.T, err error, wantCode string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error with code %q, got nil", wantCode)
	}
	f := fault.From(err)
	if f.Kind != fault.KindInvalid {
		t.Fatalf("kind = %v, want %v", f.Kind, fault.KindInvalid)
	}
	if f.Code != wantCode {
		t.Fatalf("code = %q, want %q", f.Code, wantCode)
	}
}

func TestDecodeAcceptsValidBody(t *testing.T) {
	got, err := decodeRequest(t, "application/json", `{"name":"api-1"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "api-1" {
		t.Fatalf("name = %q, want %q", got.Name, "api-1")
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := decodeRequest(t, "application/json", `{"name":"api-1","isAdmin":true}`)
	assertInvalid(t, err, "invalid_json")
}

func TestDecodeRejectsWrongContentType(t *testing.T) {
	_, err := decodeRequest(t, "text/plain", `{"name":"api-1"}`)
	assertInvalid(t, err, "unsupported_media_type")
}

func TestDecodeRejectsEmptyBody(t *testing.T) {
	_, err := decodeRequest(t, "application/json", ``)
	assertInvalid(t, err, "empty_body")
}

func TestDecodeRejectsTrailingContent(t *testing.T) {
	_, err := decodeRequest(t, "application/json", `{"name":"api-1"}{"name":"api-2"}`)
	assertInvalid(t, err, "invalid_json")
}

func TestDecodeRejectsOversizedBody(t *testing.T) {
	oversized := `{"name":"` + strings.Repeat("a", int(MaxBodyBytes)+1) + `"}`
	_, err := decodeRequest(t, "application/json", oversized)
	assertInvalid(t, err, "body_too_large")
}
