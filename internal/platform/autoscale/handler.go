package autoscale

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type setRequest struct {
	Min       int `json:"min"`
	Max       int `json:"max"`
	TargetCPU int `json:"target_cpu"`
}

type response struct {
	ID         string `json:"id"`
	ServiceID  string `json:"service_id"`
	Min        int    `json:"min"`
	Max        int    `json:"max"`
	TargetCPU  int    `json:"target_cpu"`
	LastAt     string `json:"last_at,omitempty"`
	LastReason string `json:"last_reason,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type listResponse struct {
	Autoscalers []response `json:"autoscalers"`
}

func toResponse(p Policy) response {
	body := response{
		ID:         p.ID,
		ServiceID:  p.ServiceID,
		Min:        p.Min,
		Max:        p.Max,
		TargetCPU:  p.TargetCPU,
		LastReason: p.LastReason,
		CreatedAt:  p.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:  p.UpdatedAt.Format(time.RFC3339Nano),
	}
	if !p.LastAt.IsZero() {
		body.LastAt = p.LastAt.Format(time.RFC3339Nano)
	}
	return body
}

type handler struct {
	svc *service
}

func (h *handler) set(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[setRequest](w, r)
	if err != nil {
		return err
	}

	policy, err := h.svc.set(r.Context(), PolicyParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		ServiceID: r.PathValue("id"),
		Min:       req.Min,
		Max:       req.Max,
		TargetCPU: req.TargetCPU,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(policy))
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	policy, err := h.svc.policyOf(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(policy))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	policies, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Autoscalers: make([]response, 0, len(policies))}
	for _, p := range policies {
		body.Autoscalers = append(body.Autoscalers, toResponse(p))
	}
	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) clear(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.clear(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
