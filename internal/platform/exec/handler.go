package exec

import (
	"net/http"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type startRequest struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout,omitempty"`
}

type resultRequest struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type response struct {
	ID             string   `json:"id"`
	InstanceID     string   `json:"instance_id"`
	NodeID         string   `json:"node_id,omitempty"`
	Isolation      string   `json:"isolation,omitempty"`
	Command        []string `json:"command"`
	Timeout        string   `json:"timeout"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	State          string   `json:"state"`
	Output         string   `json:"output,omitempty"`
	Truncated      bool     `json:"truncated,omitempty"`
	ExitCode       *int     `json:"exit_code,omitempty"`
	Message        string   `json:"message,omitempty"`
	CreatedAt      string   `json:"created_at"`
	EndedAt        string   `json:"ended_at,omitempty"`
}

type listResponse struct {
	Execs []response `json:"execs"`
}

func toResponse(run Run) response {
	body := response{
		ID:             run.ID,
		InstanceID:     run.InstanceID,
		NodeID:         run.NodeID,
		Isolation:      run.Isolation,
		Command:        run.Command,
		Timeout:        run.Timeout.String(),
		TimeoutSeconds: int(run.Timeout / time.Second),
		State:          run.State,
		Output:         run.Output,
		Truncated:      run.Truncated,
		ExitCode:       run.ExitCode,
		Message:        run.Message,
		CreatedAt:      run.CreatedAt.Format(time.RFC3339Nano),
	}
	if !run.EndedAt.IsZero() {
		body.EndedAt = run.EndedAt.Format(time.RFC3339Nano)
	}
	return body
}

type handler struct {
	svc *service
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[startRequest](w, r)
	if err != nil {
		return err
	}

	started, err := h.svc.start(r.Context(), StartParams{
		ProjectID:   scope.From(r.Context()).ProjectID,
		InstanceRef: r.PathValue("id"),
		Command:     req.Command,
		Timeout:     req.Timeout,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(started))
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	run, err := h.svc.readIn(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(run))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return fault.Invalid("invalid_limit", "limit must be a positive whole number")
		}
		limit = min(parsed, 200)
	}

	runs, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID, limit)
	if err != nil {
		return err
	}

	body := listResponse{Execs: make([]response, 0, len(runs))}
	for _, run := range runs {
		body.Execs = append(body.Execs, toResponse(run))
	}
	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) take(w http.ResponseWriter, r *http.Request) error {
	run, found, err := h.svc.take(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	httpx.Write(w, http.StatusOK, toResponse(run))
	return nil
}

func (h *handler) finish(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[resultRequest](w, r)
	if err != nil {
		return err
	}

	if err := h.svc.finish(r.Context(), r.PathValue("nodeID"), r.PathValue("execID"),
		Result{
			Output:    req.Output,
			Truncated: req.Truncated,
			ExitCode:  req.ExitCode,
			Message:   req.Message,
		}); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}
