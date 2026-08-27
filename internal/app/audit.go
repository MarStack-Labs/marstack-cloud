package app

import (
	"context"
	"net/http"
	"regexp"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/platform/audit"
	"github.com/marstack-labs/marstack-cloud/internal/platform/token"
)

type recorder interface {
	Record(ctx context.Context, record audit.Record)
}

func auditTrail(trail recorder) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, identity := withIdentityHolder(r.Context())
			r = r.WithContext(ctx)

			status := httpx.Observe(next, w, r)

			if !worthRecording(r.Method, r.URL.Path, identity.Role, status) {
				return
			}

			trail.Record(r.Context(), audit.Record{
				Actor:     identity.Name,
				Role:      identity.Role,
				ProjectID: identity.ProjectID,
				Method:    r.Method,
				Path:      r.URL.Path,
				Status:    status,
				RequestID: httpx.RequestIDFrom(r.Context()),
			})
		})
	}
}

var routine = regexp.MustCompile(
	`^/v1/nodes/[^/]+/(heartbeat|usage|images|volumes)$|^/v1/nodes/[^/]+/instances/[^/]+/status$`)

func worthRecording(method, path, role string, status int) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	if role == token.RoleNode && routine.MatchString(path) {
		return false
	}
	return true
}
