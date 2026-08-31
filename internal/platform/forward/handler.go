package forward

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	InstanceID string `json:"instance_id"`
	Protocol   string `json:"protocol,omitempty"`
	NodePort   int    `json:"node_port,omitempty"`
	TargetPort int    `json:"target_port"`
	Family     string `json:"family,omitempty"`
}

type response struct {
	ID         string       `json:"id"`
	InstanceID string       `json:"instance_id"`
	Protocol   string       `json:"protocol"`
	NodePort   int          `json:"node_port"`
	TargetPort int          `json:"target_port"`
	NodeID     string       `json:"node_id"`
	Address    string       `json:"address"`
	Family     string       `json:"family,omitempty"`
	TLS        *tlsResponse `json:"tls,omitempty"`
	CreatedAt  string       `json:"created_at"`
}

type tlsResponse struct {
	Subject   string `json:"subject,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type certificateRequest struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

type nodeResponse struct {
	response
	Certificate string `json:"certificate,omitempty"`
	PrivateKey  string `json:"private_key,omitempty"`
}

type nodeListResponse struct {
	Forwards []nodeResponse `json:"forwards"`
}

type listResponse struct {
	Forwards []response `json:"forwards"`
}

func toResponse(f Forward) response {
	return response{
		ID:         f.ID,
		InstanceID: f.InstanceID,
		Protocol:   f.Protocol,
		NodePort:   f.NodePort,
		TargetPort: f.TargetPort,
		NodeID:     f.NodeID,
		Address:    f.Address,
		Family:     f.Family,
		TLS:        summarise(f.TLS),
		CreatedAt:  f.CreatedAt.Format(time.RFC3339Nano),
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

	f, err := h.svc.create(r.Context(), CreateParams{
		ProjectID:  scope.From(r.Context()).ProjectID,
		InstanceID: req.InstanceID,
		Protocol:   req.Protocol,
		NodePort:   req.NodePort,
		TargetPort: req.TargetPort,
		Family:     req.Family,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(f))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	forwards, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	writeList(w, forwards)
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	forwards, err := h.svc.onNode(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	body := nodeListResponse{Forwards: make([]nodeResponse, 0, len(forwards))}
	for _, f := range forwards {
		certPEM, keyPEM, err := h.svc.certificateOf(f)
		if err != nil {
			return err
		}
		body.Forwards = append(body.Forwards, nodeResponse{
			response:    toResponse(f),
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) setCertificate(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[certificateRequest](w, r)
	if err != nil {
		return err
	}

	f, err := h.svc.setCertificate(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, req.Certificate, req.PrivateKey)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(f))
	return nil
}

func (h *handler) removeCertificate(w http.ResponseWriter, r *http.Request) error {
	f, err := h.svc.setCertificate(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, "", "")
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(f))
	return nil
}

func summarise(t TLS) *tlsResponse {
	if !t.Present() {
		return nil
	}
	return &tlsResponse{Subject: t.Subject, ExpiresAt: t.ExpiresAt}
}

func writeList(w http.ResponseWriter, forwards []Forward) {
	body := listResponse{Forwards: make([]response, 0, len(forwards))}
	for _, f := range forwards {
		body.Forwards = append(body.Forwards, toResponse(f))
	}
	httpx.Write(w, http.StatusOK, body)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
