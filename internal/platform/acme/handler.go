package acme

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type startRequest struct {
	Names []string `json:"names,omitempty"`
}

type response struct {
	ID         string   `json:"id"`
	BalancerID string   `json:"balancer_id"`
	Names      []string `json:"names"`
	State      string   `json:"state"`
	Message    string   `json:"message,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	IssuedAt   string   `json:"issued_at,omitempty"`
	CreatedAt  string   `json:"created_at"`
}

type listResponse struct {
	Certificates []response `json:"certificates"`
}

type challengeResponse struct {
	Token         string `json:"token"`
	Authorization string `json:"authorization"`
}

type challengeListResponse struct {
	Challenges []challengeResponse `json:"challenges"`
}

func toResponse(o Order) response {
	body := response{
		ID:         o.ID,
		BalancerID: o.BalancerID,
		Names:      o.Names,
		State:      o.State,
		Message:    o.Message,
		ExpiresAt:  o.ExpiresAt,
		CreatedAt:  o.CreatedAt.Format(time.RFC3339Nano),
	}
	if !o.IssuedAt.IsZero() {
		body.IssuedAt = o.IssuedAt.Format(time.RFC3339Nano)
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

	held, err := h.svc.start(r.Context(), StartParams{
		ProjectID:  scope.From(r.Context()).ProjectID,
		BalancerID: r.PathValue("id"),
		Names:      req.Names,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusAccepted, toResponse(held))
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.forBalancer(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(held))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Certificates: make([]response, 0, len(held))}
	for _, one := range held {
		body.Certificates = append(body.Certificates, toResponse(one))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	tokens, err := h.svc.tokens(r.Context())
	if err != nil {
		return err
	}

	body := challengeListResponse{Challenges: make([]challengeResponse, 0, len(tokens))}
	for token, authorization := range tokens {
		body.Challenges = append(body.Challenges, challengeResponse{
			Token:         token,
			Authorization: authorization,
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
