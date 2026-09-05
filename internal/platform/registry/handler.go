package registry

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type createRequest struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type response struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Username  string `json:"username"`
	KeyID     string `json:"key_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

type listResponse struct {
	Credentials []response `json:"credentials"`
}

type nodeResponse struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type nodeListResponse struct {
	Credentials []nodeResponse `json:"credentials"`
}

func toResponse(c Credential) response {
	return response{
		ID:        c.ID,
		Host:      c.Host,
		Username:  c.Username,
		KeyID:     c.KeyID,
		CreatedAt: c.CreatedAt.Format(time.RFC3339Nano),
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
		Host:     req.Host,
		Username: req.Username,
		Password: req.Password,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(held))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Credentials: make([]response, 0, len(held))}
	for _, one := range held {
		body.Credentials = append(body.Credentials, toResponse(one))
	}

	httpx.Write(w, http.StatusOK, body)
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
	open, err := h.svc.forNode(r.Context())
	if err != nil {
		return err
	}

	body := nodeListResponse{Credentials: make([]nodeResponse, 0, len(open))}
	for _, one := range open {
		body.Credentials = append(body.Credentials, nodeResponse{
			Host:     one.Host,
			Username: one.Username,
			Password: one.Password,
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
