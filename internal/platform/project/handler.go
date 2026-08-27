package project

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type createRequest struct {
	Name string `json:"name"`
}

type response struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type listResponse struct {
	Projects []response `json:"projects"`
}

func toResponse(p Project) response {
	return response{
		ID:        p.ID,
		Name:      p.Name,
		CreatedAt: p.CreatedAt.Format(time.RFC3339Nano),
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

	p, err := h.svc.create(r.Context(), CreateParams{Name: req.Name})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(p))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	projects, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Projects: make([]response, 0, len(projects))}
	for _, p := range projects {
		body.Projects = append(body.Projects, toResponse(p))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	p, err := h.svc.get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(p))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
