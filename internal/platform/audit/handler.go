package audit

import (
	"net/http"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type response struct {
	At        string `json:"at"`
	Actor     string `json:"actor"`
	Role      string `json:"role"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	RequestID string `json:"request_id,omitempty"`
}

type listResponse struct {
	Entries []response `json:"entries"`
}

type handler struct {
	svc *service
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil {
			limit = parsed
		}
	}

	entries, err := h.svc.list(r.Context(), limit)
	if err != nil {
		return err
	}

	body := listResponse{Entries: make([]response, 0, len(entries))}
	for _, entry := range entries {
		body.Entries = append(body.Entries, response{
			At:        entry.At.Format(time.RFC3339Nano),
			Actor:     entry.Actor,
			Role:      entry.Role,
			Method:    entry.Method,
			Path:      entry.Path,
			Status:    entry.Status,
			RequestID: entry.RequestID,
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
