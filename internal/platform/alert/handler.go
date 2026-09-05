package alert

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name       string  `json:"name"`
	InstanceID string  `json:"instance_id"`
	Metric     string  `json:"metric"`
	Comparison string  `json:"comparison,omitempty"`
	Threshold  float64 `json:"threshold"`
	For        string  `json:"for,omitempty"`
}

type response struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	InstanceID string  `json:"instance_id"`
	Metric     string  `json:"metric"`
	Comparison string  `json:"comparison"`
	Threshold  float64 `json:"threshold"`
	For        string  `json:"for"`
	State      string  `json:"state"`
	LastValue  float64 `json:"last_value"`
	Message    string  `json:"message,omitempty"`
	Since      string  `json:"since"`
	CreatedAt  string  `json:"created_at"`
}

type listResponse struct {
	Alerts []response `json:"alerts"`
}

func toResponse(a Alert) response {
	return response{
		ID:         a.ID,
		Name:       a.Name,
		InstanceID: a.InstanceID,
		Metric:     a.Metric,
		Comparison: a.Comparison,
		Threshold:  a.Threshold,
		For:        a.For.String(),
		State:      a.State,
		LastValue:  a.LastValue,
		Message:    a.Message,
		Since:      a.Since.Format(time.RFC3339Nano),
		CreatedAt:  a.CreatedAt.Format(time.RFC3339Nano),
	}
}

type handler struct {
	svc *service
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[createRequest](w, r)
	if err != nil {
		return err
	}

	held, err := h.svc.create(r.Context(), CreateParams{
		ProjectID:  scope.From(r.Context()).ProjectID,
		Name:       req.Name,
		InstanceID: req.InstanceID,
		Metric:     req.Metric,
		Comparison: req.Comparison,
		Threshold:  req.Threshold,
		For:        req.For,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(held))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Alerts: make([]response, 0, len(held))}
	for _, one := range held {
		body.Alerts = append(body.Alerts, toResponse(one))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.get(r.Context(), scope.From(r.Context()).ProjectID, r.PathValue("id"))
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(held))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id")); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}
