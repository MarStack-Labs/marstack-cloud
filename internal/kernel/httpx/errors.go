package httpx

import (
	"log/slog"
	"net/http"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

func statusFor(kind fault.Kind) int {
	switch kind {
	case fault.KindInvalid:
		return http.StatusBadRequest
	case fault.KindNotFound:
		return http.StatusNotFound
	case fault.KindConflict:
		return http.StatusConflict
	case fault.KindUnauthenticated:
		return http.StatusUnauthorized
	case fault.KindForbidden:
		return http.StatusForbidden
	case fault.KindUnavailable:
		return http.StatusServiceUnavailable
	case fault.KindTooMany:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

type Handler func(http.ResponseWriter, *http.Request) error

func Wrap(log *slog.Logger, h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h(w, r)
		if err == nil {
			return
		}

		f := fault.From(err)
		status := statusFor(f.Kind)

		if status >= http.StatusInternalServerError {
			log.ErrorContext(r.Context(), "request failed",
				"method", r.Method,
				"path", r.URL.Path,
				"request_id", RequestIDFrom(r.Context()),
				"error", f.Error(),
			)
		}

		WriteFault(w, err)
	}
}

func WriteFault(w http.ResponseWriter, err error) {
	f := fault.From(err)

	Write(w, statusFor(f.Kind), map[string]any{
		"error": map[string]string{
			"code":    f.Code,
			"message": f.Message,
		},
	})
}
