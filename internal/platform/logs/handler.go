package logs

import (
	"net/http"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type reportRequest struct {
	Lines []string `json:"lines"`
}

type lineResponse struct {
	Seq        int64  `json:"seq"`
	Text       string `json:"text"`
	ReceivedAt string `json:"received_at"`
}

type tailResponse struct {
	InstanceID string         `json:"instance_id"`
	Lines      []lineResponse `json:"lines"`
	Truncated  bool           `json:"truncated"`
}

type handler struct {
	svc *service
}

func (h *handler) report(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[reportRequest](w, r)
	if err != nil {
		return err
	}

	if err := h.svc.report(r.Context(), ReportParams{
		NodeID:     r.PathValue("nodeID"),
		InstanceID: r.PathValue("instanceID"),
		Texts:      req.Lines,
	}); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) tail(w http.ResponseWriter, r *http.Request) error {
	limit := 0
	if raw := r.URL.Query().Get("tail"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return fault.Invalid("invalid_tail", "tail must be a number of lines")
		}
		limit = parsed
	}

	instanceID, lines, err := h.svc.tailIn(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, limit)
	if err != nil {
		return err
	}

	body := tailResponse{
		InstanceID: instanceID,
		Lines:      make([]lineResponse, 0, len(lines)),
		Truncated:  len(lines) == MaxLinesPerInstance,
	}
	for _, line := range lines {
		body.Lines = append(body.Lines, lineResponse{
			Seq:        line.Seq,
			Text:       line.Text,
			ReceivedAt: line.ReceivedAt.Format(time.RFC3339Nano),
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
