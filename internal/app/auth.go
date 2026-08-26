package app

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

type verifier interface {
	Verify(ctx context.Context, secret string) (token.Identity, error)
}

var openPaths = map[string]bool{
	"/healthz": true,
}

var nodePaths = []string{
	"POST /v1/nodes/register",
	"POST /v1/nodes/{id}/heartbeat",
	"GET /v1/nodes",
	"GET /v1/nodes/{id}/instances",
	"PUT /v1/nodes/{id}/instances/{instanceID}/status",
	"GET /v1/nodes/{id}/network",
	"GET /v1/nodes/{id}/volumes",
	"PUT /v1/nodes/{id}/volumes",
	"PUT /v1/nodes/{id}/images",
	"GET /v1/dns/records",
	"GET /v1/images",
}

func authenticate(verify verifier, log *slog.Logger) httpx.Middleware {
	allowed := nodeRules()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if openPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			identity, err := verify.Verify(r.Context(), bearerOf(r))
			if err != nil {
				httpx.WriteFault(w, err)
				return
			}

			if identity.Role != token.RoleAdmin && !allowed(r) {
				log.Warn("a node token was refused an operator endpoint",
					"token", identity.Name, "method", r.Method, "path", r.URL.Path,
					"request_id", httpx.RequestIDFrom(r.Context()))
				httpx.WriteFault(w, fault.Forbidden("role_forbidden",
					"a node token may only call the endpoints an agent needs"))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func nodeRules() func(*http.Request) bool {
	mux := http.NewServeMux()
	for _, pattern := range nodePaths {
		mux.Handle(pattern, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	}

	return func(r *http.Request) bool {
		_, pattern := mux.Handler(r)
		return pattern != ""
	}
}

func bearerOf(r *http.Request) string {
	header := r.Header.Get("Authorization")
	scheme, secret, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(secret)
}
