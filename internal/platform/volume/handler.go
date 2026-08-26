package volume

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type createRequest struct {
	Name    string `json:"name"`
	SizeGiB int    `json:"size_gib"`
}

type attachRequest struct {
	InstanceID string `json:"instance_id"`
}

type response struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SizeGiB    int    `json:"size_gib"`
	State      string `json:"state"`
	NodeID     string `json:"node_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type listResponse struct {
	Volumes []response `json:"volumes"`
}

func toResponse(v Volume) response {
	return response{
		ID:         v.ID,
		Name:       v.Name,
		SizeGiB:    v.SizeGiB,
		State:      v.State(),
		NodeID:     v.NodeID,
		InstanceID: v.InstanceID,
		CreatedAt:  v.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:  v.UpdatedAt.Format(time.RFC3339Nano),
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

	v, err := h.svc.create(r.Context(), CreateParams{Name: req.Name, SizeGiB: req.SizeGiB})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(v))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	volumes, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Volumes: make([]response, 0, len(volumes))}
	for _, v := range volumes {
		body.Volumes = append(body.Volumes, toResponse(v))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(v))
	return nil
}

func (h *handler) attach(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[attachRequest](w, r)
	if err != nil {
		return err
	}

	v, err := h.svc.attach(r.Context(), r.PathValue("id"), req.InstanceID)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(v))
	return nil
}

func (h *handler) detach(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.detach(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(v))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	volumes, err := h.svc.onNode(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	body := listResponse{Volumes: make([]response, 0, len(volumes))}
	for _, v := range volumes {
		body.Volumes = append(body.Volumes, toResponse(v))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
