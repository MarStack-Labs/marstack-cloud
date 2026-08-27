package firewall

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type ruleBody struct {
	Protocol string `json:"protocol,omitempty"`
	FromPort int    `json:"from_port,omitempty"`
	ToPort   int    `json:"to_port,omitempty"`
	Source   string `json:"source,omitempty"`
}

type createRequest struct {
	Name  string     `json:"name"`
	Rules []ruleBody `json:"rules,omitempty"`
}

type rulesRequest struct {
	Rules []ruleBody `json:"rules"`
}

type response struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Rules     []ruleBody `json:"rules"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

type listResponse struct {
	Firewalls []response `json:"firewalls"`
}

func toResponse(f Firewall) response {
	rules := make([]ruleBody, 0, len(f.Rules))
	for _, rule := range f.Rules {
		rules = append(rules, ruleBody{
			Protocol: rule.Protocol,
			FromPort: rule.FromPort,
			ToPort:   rule.ToPort,
			Source:   rule.Source,
		})
	}

	return response{
		ID:        f.ID,
		Name:      f.Name,
		Rules:     rules,
		CreatedAt: f.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt: f.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func toRules(bodies []ruleBody) []Rule {
	rules := make([]Rule, 0, len(bodies))
	for _, body := range bodies {
		rules = append(rules, Rule{
			Protocol: body.Protocol,
			FromPort: body.FromPort,
			ToPort:   body.ToPort,
			Source:   body.Source,
		})
	}
	return rules
}

type handler struct {
	svc *service
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[createRequest](w, r)
	if err != nil {
		return err
	}

	f, err := h.svc.create(r.Context(), CreateParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		Name:      req.Name,
		Rules:     toRules(req.Rules),
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(f))
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	firewalls, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Firewalls: make([]response, 0, len(firewalls))}
	for _, f := range firewalls {
		body.Firewalls = append(body.Firewalls, toResponse(f))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	firewalls, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Firewalls: make([]response, 0, len(firewalls))}
	for _, f := range firewalls {
		body.Firewalls = append(body.Firewalls, toResponse(f))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	f, err := h.svc.resolveIn(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(f))
	return nil
}

func (h *handler) setRules(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[rulesRequest](w, r)
	if err != nil {
		return err
	}

	f, err := h.svc.setRules(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, toRules(req.Rules))
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(f))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
