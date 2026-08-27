package balancer

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name       string   `json:"name"`
	Protocol   string   `json:"protocol,omitempty"`
	ListenPort int      `json:"listen_port,omitempty"`
	TargetPort int      `json:"target_port"`
	Algorithm  string   `json:"algorithm,omitempty"`
	Instances  []string `json:"instances,omitempty"`
}

type backendRequest struct {
	InstanceID string `json:"instance_id"`
}

type backendResponse struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address,omitempty"`
	Healthy    bool   `json:"healthy"`
	AddedAt    string `json:"added_at"`
}

type response struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Protocol   string            `json:"protocol"`
	ListenPort int               `json:"listen_port"`
	TargetPort int               `json:"target_port"`
	Algorithm  string            `json:"algorithm"`
	Backends   []backendResponse `json:"backends"`
	CreatedAt  string            `json:"created_at"`
}

type listResponse struct {
	Balancers []response `json:"balancers"`
}

func toResponse(b Balancer) response {
	backends := make([]backendResponse, 0, len(b.Backends))
	for _, backend := range b.Backends {
		backends = append(backends, backendResponse{
			InstanceID: backend.InstanceID,
			Address:    backend.Address,
			Healthy:    backend.Healthy,
			AddedAt:    backend.AddedAt.Format(time.RFC3339Nano),
		})
	}

	return response{
		ID:         b.ID,
		Name:       b.Name,
		Protocol:   b.Protocol,
		ListenPort: b.ListenPort,
		TargetPort: b.TargetPort,
		Algorithm:  b.Algorithm,
		Backends:   backends,
		CreatedAt:  b.CreatedAt.Format(time.RFC3339Nano),
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

	b, err := h.svc.create(r.Context(), CreateParams{
		ProjectID:  scope.From(r.Context()).ProjectID,
		Name:       req.Name,
		Protocol:   req.Protocol,
		ListenPort: req.ListenPort,
		TargetPort: req.TargetPort,
		Algorithm:  req.Algorithm,
		Instances:  req.Instances,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(b))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	balancers, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	writeList(w, balancers)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	b, err := h.svc.get(r.Context(), scope.From(r.Context()).ProjectID, r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	balancers, err := h.svc.forNode(r.Context())
	if err != nil {
		return err
	}
	writeList(w, balancers)
	return nil
}

func writeList(w http.ResponseWriter, balancers []Balancer) {
	body := listResponse{Balancers: make([]response, 0, len(balancers))}
	for _, b := range balancers {
		body.Balancers = append(body.Balancers, toResponse(b))
	}
	httpx.Write(w, http.StatusOK, body)
}

func (h *handler) addBackend(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[backendRequest](w, r)
	if err != nil {
		return err
	}

	b, err := h.svc.addBackend(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), req.InstanceID)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) removeBackend(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.removeBackend(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), r.PathValue("instanceID")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
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
