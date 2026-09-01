package audit

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/page"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type response struct {
	ID        int64  `json:"id"`
	At        string `json:"at"`
	Actor     string `json:"actor"`
	UserID    string `json:"user_id,omitempty"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id,omitempty"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	RequestID string `json:"request_id,omitempty"`
}

type listResponse struct {
	Entries []response `json:"entries"`
	Next    string     `json:"next,omitempty"`
}

type handler struct {
	svc *service
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	window, err := page.From(r, DefaultLimit, MaxLimit)
	if err != nil {
		return fault.Invalid("invalid_page", err.Error())
	}

	before, err := page.Back(window.After)
	if err != nil {
		return fault.Invalid("invalid_page", err.Error())
	}

	entries, err := h.svc.list(r.Context(), window.Limit,
		scope.From(r.Context()).ProjectID, before)
	if err != nil {
		return err
	}

	body := listResponse{Entries: make([]response, 0, len(entries))}
	for _, entry := range entries {
		body.Entries = append(body.Entries, response{
			ID:        entry.ID,
			At:        entry.At.Format(time.RFC3339Nano),
			Actor:     entry.Actor,
			UserID:    entry.UserID,
			Role:      entry.Role,
			ProjectID: entry.ProjectID,
			Method:    entry.Method,
			Path:      entry.Path,
			Status:    entry.Status,
			RequestID: entry.RequestID,
		})
	}

	if len(entries) == window.Limit {
		body.Next = page.Backward(entries[len(entries)-1].ID)
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
