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
	"/healthz":  true,
	"/v1/login": true,
}

var unlimitedPaths = map[string]bool{
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
	"GET /v1/nodes/{id}/volumes/{volumeID}/key",
	"GET /v1/nodes/{id}/forwards",
	"GET /v1/nodes/{id}/balancers",
	"PUT /v1/nodes/{id}/balancers/health",
	"PUT /v1/nodes/{id}/volumes",
	"PUT /v1/nodes/{id}/images",
	"PUT /v1/nodes/{id}/usage",
	"GET /v1/nodes/{id}/exec",
	"POST /v1/nodes/{id}/execs/{execID}/result",
	"PUT /v1/nodes/{id}/instances/{instanceID}/logs",
	"PUT /v1/nodes/{id}/instances/{instanceID}/disk",
	"GET /v1/nodes/{id}/instances/{instanceID}/disk",
	"POST /v1/nodes/{id}/instances/{instanceID}/landed",
	"GET /v1/nodes/{id}/images",
	"GET /v1/nodes/{id}/registries",
	"GET /v1/nodes/{id}/acme-challenges",
	"GET /v1/nodes/{id}/shell",
	"POST /v1/nodes/{id}/shells/{execID}/output",
	"GET /v1/nodes/{id}/shells/{execID}/input",
	"GET /v1/nodes/{id}/firewalls",
	"GET /v1/nodes/{id}/backups",
	"PUT /v1/nodes/{id}/backups/{backupID}/content",
	"GET /v1/nodes/{id}/backups/{backupID}/upload",
	"GET /v1/nodes/{id}/backups/{backupID}/download",
	"POST /v1/nodes/{id}/backups/{backupID}/uploaded",
	"POST /v1/nodes/{id}/backups/{backupID}/failure",
	"GET /v1/nodes/{id}/backups/{backupID}/content",
	"GET /v1/dns/records",
}

var streamingPaths = []string{
	"GET /v1/shells/{id}/output",
	"POST /v1/shells/{id}/input",
	"POST /v1/nodes/{id}/shells/{execID}/output",
	"GET /v1/nodes/{id}/shells/{execID}/input",
	"PUT /v1/nodes/{id}/backups/{backupID}/content",
	"GET /v1/nodes/{id}/backups/{backupID}/content",
	"PUT /v1/nodes/{id}/instances/{instanceID}/disk",
	"GET /v1/nodes/{id}/instances/{instanceID}/disk",
}

var memberPaths = []string{
	"GET /v1/version",
	"GET /v1/usage",
	"GET /v1/usage/history",
	"GET /v1/quota",
	"GET /v1/dns/records",
	"GET /v1/events",

	"GET /v1/webhooks",
	"GET /v1/webhooks/{id}",
	"GET /v1/webhooks/{id}/deliveries",
	"POST /v1/webhooks",
	"POST /v1/webhooks/{id}/pause",
	"POST /v1/webhooks/{id}/resume",
	"DELETE /v1/webhooks/{id}",

	"POST /v1/jobs",
	"GET /v1/jobs",
	"GET /v1/jobs/{id}",
	"GET /v1/jobs/{id}/runs",
	"POST /v1/jobs/{id}/run",
	"POST /v1/jobs/{id}/pause",
	"POST /v1/jobs/{id}/resume",
	"DELETE /v1/jobs/{id}",
	"POST /v1/users",
	"GET /v1/users",
	"GET /v1/users/{id}",
	"PUT /v1/users/{id}/password",
	"POST /v1/users/{id}/disable",
	"POST /v1/users/{id}/enable",
	"DELETE /v1/users/{id}",
	"GET /v1/services",
	"GET /v1/services/{id}",
	"POST /v1/services",
	"POST /v1/services/{id}/scale",
	"PUT /v1/services/{id}/template",
	"PUT /v1/services/{id}/autoscale",
	"GET /v1/services/{id}/autoscale",
	"DELETE /v1/services/{id}/autoscale",
	"GET /v1/autoscalers",
	"DELETE /v1/services/{id}",

	"GET /v1/keys",
	"POST /v1/keys",
	"DELETE /v1/keys/{id}",

	"GET /v1/instances",
	"POST /v1/instances",
	"POST /v1/instances/{id}/resize",
	"GET /v1/instances/{id}",
	"GET /v1/instances/{id}/logs",
	"POST /v1/instances/{id}/exec",
	"GET /v1/execs",
	"GET /v1/execs/{id}",
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
	"POST /v1/volumes/{id}/resize",
	"GET /v1/volumes/{id}/snapshots",
	"POST /v1/volumes/{id}/snapshots",

	"GET /v1/volumes/{id}/backups",
	"POST /v1/volumes/{id}/backups",
	"GET /v1/volumes/{id}/schedule",
	"PUT /v1/volumes/{id}/schedule",
	"DELETE /v1/volumes/{id}/schedule",
	"GET /v1/backup-schedules",
	"GET /v1/snapshot-schedules",
	"PUT /v1/volumes/{id}/snapshot-schedule",
	"DELETE /v1/volumes/{id}/snapshot-schedule",

	"GET /v1/backups",
	"GET /v1/backups/{id}",
	"DELETE /v1/backups/{id}",

	"GET /v1/snapshots",
	"DELETE /v1/snapshots/{id}",
	"POST /v1/snapshots/{id}/restore",
	"POST /v1/snapshots/{id}/clone",

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
	"PUT /v1/forwards/{id}/certificate",
	"DELETE /v1/forwards/{id}/certificate",
	"DELETE /v1/forwards/{id}",

	"GET /v1/balancers",
	"GET /v1/balancers/{id}",
	"POST /v1/balancers",
	"PUT /v1/balancers/{id}/routes",
	"POST /v1/balancers/{id}/certificate/acme",
	"GET /v1/balancers/{id}/certificate/acme",
	"DELETE /v1/balancers/{id}/certificate/acme",
	"GET /v1/certificates",
	"POST /v1/alerts",
	"GET /v1/alerts",
	"GET /v1/alerts/{id}",
	"DELETE /v1/alerts/{id}",
	"POST /v1/instances/{id}/shell",
	"GET /v1/shells",
	"GET /v1/shells/{id}",
	"DELETE /v1/shells/{id}",
	"GET /v1/shells/{id}/output",
	"POST /v1/shells/{id}/input",
	"PUT /v1/balancers/{id}/certificate",
	"DELETE /v1/balancers/{id}/certificate",
	"DELETE /v1/balancers/{id}",
	"POST /v1/balancers/{id}/backends",
	"DELETE /v1/balancers/{id}/backends/{instanceID}",
}

func readsOf(patterns []string) []string {
	reads := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "GET ") || strings.HasPrefix(pattern, "HEAD ") {
			reads = append(reads, pattern)
		}
	}
	return reads
}

func authenticate(verify verifier, log *slog.Logger) httpx.Middleware {
	allowed := map[string]func(*http.Request) bool{
		token.RoleNode:   allowList(nodePaths),
		token.RoleMember: allowList(memberPaths),
		token.RoleViewer: allowList(readsOf(memberPaths)),
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
				UserID:    identity.UserID,
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
