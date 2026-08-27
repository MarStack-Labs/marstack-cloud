package app

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

type verifier interface {
	Verify(ctx context.Context, secret string) (token.Identity, error)
}

type identityKey struct{}

func withIdentityHolder(ctx context.Context) (context.Context, *token.Identity) {
	holder := &token.Identity{}
	return context.WithValue(ctx, identityKey{}, holder), holder
}

func noteIdentity(ctx context.Context, identity token.Identity) {
	if holder, ok := ctx.Value(identityKey{}).(*token.Identity); ok {
		*holder = identity
	}
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
	"GET /v1/nodes/{id}/forwards",
	"PUT /v1/nodes/{id}/volumes",
	"PUT /v1/nodes/{id}/images",
	"PUT /v1/nodes/{id}/usage",
	"GET /v1/nodes/{id}/images",
	"GET /v1/nodes/{id}/firewalls",
	"GET /v1/nodes/{id}/backups",
	"PUT /v1/nodes/{id}/backups/{backupID}/content",
	"POST /v1/nodes/{id}/backups/{backupID}/failure",
	"GET /v1/nodes/{id}/backups/{backupID}/content",
	"GET /v1/dns/records",
}

var streamingPaths = []string{
	"PUT /v1/nodes/{id}/backups/{backupID}/content",
	"GET /v1/nodes/{id}/backups/{backupID}/content",
}

var memberPaths = []string{
	"GET /v1/version",
	"GET /v1/usage",
	"GET /v1/dns/records",

	"GET /v1/instances",
	"POST /v1/instances",
	"GET /v1/instances/{id}",
	"DELETE /v1/instances/{id}",
	"POST /v1/instances/{id}/start",
	"POST /v1/instances/{id}/stop",

	"GET /v1/networks",
	"POST /v1/networks",
	"GET /v1/networks/{id}",
	"DELETE /v1/networks/{id}",

	"GET /v1/volumes",
	"POST /v1/volumes",
	"GET /v1/volumes/{id}",
	"DELETE /v1/volumes/{id}",
	"POST /v1/volumes/{id}/attach",
	"POST /v1/volumes/{id}/detach",
	"GET /v1/volumes/{id}/snapshots",
	"POST /v1/volumes/{id}/snapshots",

	"GET /v1/volumes/{id}/backups",
	"POST /v1/volumes/{id}/backups",

	"GET /v1/backups",
	"GET /v1/backups/{id}",
	"DELETE /v1/backups/{id}",

	"GET /v1/snapshots",
	"DELETE /v1/snapshots/{id}",
	"POST /v1/snapshots/{id}/restore",

	"GET /v1/images",
	"POST /v1/images",
	"GET /v1/images/{id}",
	"DELETE /v1/images/{id}",

	"GET /v1/firewalls",
	"POST /v1/firewalls",
	"GET /v1/firewalls/{id}",
	"PUT /v1/firewalls/{id}/rules",
	"DELETE /v1/firewalls/{id}",

	"GET /v1/forwards",
	"POST /v1/forwards",
	"DELETE /v1/forwards/{id}",
}

func authenticate(verify verifier, log *slog.Logger) httpx.Middleware {
	allowed := map[string]func(*http.Request) bool{
		token.RoleNode:   allowList(nodePaths),
		token.RoleMember: allowList(memberPaths),
	}

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

			noteIdentity(r.Context(), identity)

			if identity.Role != token.RoleAdmin {
				reachable, known := allowed[identity.Role]
				if !known || !reachable(r) {
					log.Warn("a token was refused an endpoint its role cannot reach",
						"token", identity.Name, "role", identity.Role,
						"method", r.Method, "path", r.URL.Path,
						"request_id", httpx.RequestIDFrom(r.Context()))
					httpx.WriteFault(w, fault.Forbidden("role_forbidden",
						"this token's role may not call that endpoint"))
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(scope.With(r.Context(), scope.Scope{
				ProjectID: identity.ProjectID,
				TokenID:   identity.ID,
				TokenName: identity.Name,
				Role:      identity.Role,
			})))
		})
	}
}

func allowList(patterns []string) func(*http.Request) bool {
	mux := http.NewServeMux()
	for _, pattern := range patterns {
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
