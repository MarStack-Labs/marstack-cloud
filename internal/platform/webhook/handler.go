package webhook

import (
	"net/http"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name  string   `json:"name"`
	URL   string   `json:"url"`
	Kinds []string `json:"kinds,omitempty"`
}

type response struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Kinds     []string `json:"kinds"`
	Active    bool     `json:"active"`
	Secret    string   `json:"secret,omitempty"`
	CreatedAt string   `json:"created_at"`
}

type listResponse struct {
	Webhooks []response `json:"webhooks"`
}

type deliveryResponse struct {
	ID            string `json:"id"`
	EventID       int64  `json:"event_id"`
	Kind          string `json:"kind"`
	Subject       string `json:"subject,omitempty"`
	State         string `json:"state"`
	Attempts      int    `json:"attempts"`
	LastError     string `json:"last_error,omitempty"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	CreatedAt     string `json:"created_at"`
}

type deliveryListResponse struct {
	Deliveries []deliveryResponse `json:"deliveries"`
}

func toResponse(s Subscription, withSecret bool) response {
	body := response{
		ID:        s.ID,
		Name:      s.Name,
		URL:       s.URL,
		Kinds:     s.Kinds,
		Active:    s.Active,
		CreatedAt: s.CreatedAt.Format(time.RFC3339Nano),
	}
	if body.Kinds == nil {
		body.Kinds = []string{}
	}
	if withSecret {
		body.Secret = s.Secret
	}
	return body
}

type handler struct {
	svc *service
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[createRequest](w, r)
	if err != nil {
		return err
	}

	created, err := h.svc.create(r.Context(), CreateParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		Name:      req.Name,
		URL:       req.URL,
		Kinds:     req.Kinds,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(created, true))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	subs, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Webhooks: make([]response, 0, len(subs))}
	for _, sub := range subs {
		body.Webhooks = append(body.Webhooks, toResponse(sub, false))
	}
	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	found, err := h.svc.find(r.Context(), scope.From(r.Context()).ProjectID, r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(found, false))
	return nil
}

func (h *handler) pause(w http.ResponseWriter, r *http.Request) error {
	return h.setActive(w, r, false)
}

func (h *handler) resume(w http.ResponseWriter, r *http.Request) error {
	return h.setActive(w, r, true)
}

func (h *handler) setActive(w http.ResponseWriter, r *http.Request, active bool) error {
	found, err := h.svc.setActive(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), active)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(found, false))
	return nil
}

func (h *handler) deliveries(w http.ResponseWriter, r *http.Request) error {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 0
	}

	found, err := h.svc.deliveries(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), limit)
	if err != nil {
		return err
	}

	body := deliveryListResponse{Deliveries: make([]deliveryResponse, 0, len(found))}
	for _, d := range found {
		entry := deliveryResponse{
			ID:        d.ID,
			EventID:   d.EventID,
			Kind:      d.Kind,
			Subject:   d.Subject,
			State:     d.State,
			Attempts:  d.Attempts,
			LastError: d.LastError,
			CreatedAt: d.CreatedAt.Format(time.RFC3339Nano),
		}
		if d.State == StatePending {
			entry.NextAttemptAt = d.NextAttemptAt.Format(time.RFC3339Nano)
		}
		body.Deliveries = append(body.Deliveries, entry)
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
