package keypair

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

type response struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"`
	Comment     string `json:"comment,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type listResponse struct {
	Keys []response `json:"keys"`
}

func toResponse(k Key) response {
	return response{
		ID:          k.ID,
		Name:        k.Name,
		Fingerprint: k.Fingerprint,
		Kind:        k.Kind,
		Comment:     k.Comment,
		CreatedAt:   k.CreatedAt.Format(time.RFC3339Nano),
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

	k, err := h.svc.create(r.Context(), CreateParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		Name:      req.Name,
		PublicKey: req.PublicKey,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(k))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	keys, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Keys: make([]response, 0, len(keys))}
	for _, k := range keys {
		body.Keys = append(body.Keys, toResponse(k))
	}

	httpx.Write(w, http.StatusOK, body)
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
