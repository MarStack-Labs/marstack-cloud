package user

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type createRequest struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id,omitempty"`
	Password  string `json:"password"`
}

type passwordRequest struct {
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type response struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type listResponse struct {
	Users []response `json:"users"`
}

type sessionResponse struct {
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id,omitempty"`
	ExpiresAt string `json:"expires_at"`
}

func toResponse(u User) response {
	return response{
		ID:        u.ID,
		Email:     u.Email,
		Name:      u.Name,
		Role:      u.Role,
		ProjectID: u.ProjectID,
		Disabled:  u.Disabled,
		CreatedAt: u.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt: u.UpdatedAt.Format(time.RFC3339Nano),
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

	created, err := h.svc.create(r.Context(), CreateParams{
		Email:     req.Email,
		Name:      req.Name,
		Role:      req.Role,
		ProjectID: req.ProjectID,
		Password:  req.Password,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(created))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	people, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Users: make([]response, 0, len(people))}
	for _, person := range people {
		body.Users = append(body.Users, toResponse(person))
	}
	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	found, err := h.svc.find(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(found))
	return nil
}

func (h *handler) setPassword(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[passwordRequest](w, r)
	if err != nil {
		return err
	}
	if err := h.svc.setPassword(r.Context(), r.PathValue("id"), req.Password); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) disable(w http.ResponseWriter, r *http.Request) error {
	return h.setDisabled(w, r, true)
}

func (h *handler) enable(w http.ResponseWriter, r *http.Request) error {
	return h.setDisabled(w, r, false)
}

func (h *handler) setDisabled(w http.ResponseWriter, r *http.Request, disabled bool) error {
	changed, err := h.svc.setDisabled(r.Context(), r.PathValue("id"), disabled)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(changed))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[loginRequest](w, r)
	if err != nil {
		return err
	}

	session, err := h.svc.login(r.Context(), r.RemoteAddr, req.Email, req.Password)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, sessionResponse{
		Token:     session.Secret,
		UserID:    session.UserID,
		Email:     session.Email,
		Role:      session.Role,
		ProjectID: session.ProjectID,
		ExpiresAt: session.ExpiresAt.Format(time.RFC3339Nano),
	})
	return nil
}
