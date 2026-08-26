package token

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type createRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type response struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Secret     string `json:"secret,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
}

type listResponse struct {
	Tokens []response `json:"tokens"`
}

func toResponse(t Token, secret string) response {
	return response{
		ID:         t.ID,
		Name:       t.Name,
		Role:       t.Role,
		Secret:     secret,
		CreatedAt:  t.CreatedAt.Format(time.RFC3339Nano),
		LastUsedAt: t.LastUsedAt.Format(time.RFC3339Nano),
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

	t, secret, err := h.svc.create(r.Context(), CreateParams{Name: req.Name, Role: req.Role})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(t, secret))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	tokens, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Tokens: make([]response, 0, len(tokens))}
	for _, t := range tokens {
		body.Tokens = append(body.Tokens, toResponse(t, ""))
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
