package event

import (
	"net/http"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type response struct {
	ID       int64  `json:"id"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject,omitempty"`
	NodeID   string `json:"node_id,omitempty"`
	Message  string `json:"message,omitempty"`
	Severity string `json:"severity"`
}

type listResponse struct {
	Events []response `json:"events"`
}

type handler struct {
	svc *service
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 0
	}

	entries, err := h.svc.list(r.Context(), Filter{
		ProjectID: scope.From(r.Context()).ProjectID,
		Subject:   r.URL.Query().Get("subject"),
		Kind:      r.URL.Query().Get("kind"),
		Severity:  r.URL.Query().Get("severity"),
		Limit:     limit,
	})
	if err != nil {
		return err
	}

	body := listResponse{Events: make([]response, 0, len(entries))}
	for _, entry := range entries {
		body.Events = append(body.Events, response{
			ID:       entry.ID,
			At:       entry.At.Format(time.RFC3339Nano),
			Kind:     entry.Kind,
			Subject:  entry.Subject,
			NodeID:   entry.NodeID,
			Message:  entry.Message,
			Severity: entry.Severity,
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
