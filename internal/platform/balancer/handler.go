package balancer

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name       string         `json:"name"`
	Protocol   string         `json:"protocol,omitempty"`
	ListenPort int            `json:"listen_port,omitempty"`
	TargetPort int            `json:"target_port"`
	Algorithm  string         `json:"algorithm,omitempty"`
	Service    string         `json:"service,omitempty"`
	Check      string         `json:"check,omitempty"`
	CheckPath  string         `json:"check_path,omitempty"`
	Rise       int            `json:"rise,omitempty"`
	Fall       int            `json:"fall,omitempty"`
	Family     string         `json:"family,omitempty"`
	Instances  []string       `json:"instances,omitempty"`
	Routes     []routeRequest `json:"routes,omitempty"`
}

type routeRequest struct {
	Host    string `json:"host,omitempty"`
	Path    string `json:"path,omitempty"`
	Service string `json:"service"`
}

type routeResponse struct {
	Host     string            `json:"host,omitempty"`
	Path     string            `json:"path,omitempty"`
	Service  string            `json:"service"`
	Backends []backendResponse `json:"backends"`
}

type healthRequest struct {
	Checks []healthEntry `json:"checks"`
}

type healthEntry struct {
	BalancerID string `json:"balancer_id"`
	InstanceID string `json:"instance_id"`
	Healthy    bool   `json:"healthy"`
	Reason     string `json:"reason,omitempty"`
}

type backendRequest struct {
	InstanceID string `json:"instance_id"`
}

type backendResponse struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address,omitempty"`
	Healthy    bool   `json:"healthy"`
	Running    bool   `json:"running"`
	Probe      string `json:"probe,omitempty"`
	Reason     string `json:"reason,omitempty"`
	CheckedAt  string `json:"checked_at,omitempty"`
	AddedAt    string `json:"added_at"`
}

type response struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Protocol   string            `json:"protocol"`
	ListenPort int               `json:"listen_port"`
	TargetPort int               `json:"target_port"`
	Algorithm  string            `json:"algorithm"`
	Service    string            `json:"service,omitempty"`
	Check      string            `json:"check"`
	CheckPath  string            `json:"check_path,omitempty"`
	Rise       int               `json:"rise,omitempty"`
	Fall       int               `json:"fall,omitempty"`
	Backends   []backendResponse `json:"backends"`
	Routes     []routeResponse   `json:"routes,omitempty"`
	Family     string            `json:"family,omitempty"`
	TLS        *tlsResponse      `json:"tls,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

type tlsResponse struct {
	Subject   string `json:"subject,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type routesRequest struct {
	Routes []routeRequest `json:"routes"`
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
	Balancers []nodeResponse `json:"balancers"`
}

type listResponse struct {
	Balancers []response `json:"balancers"`
}

func toBackends(held []Backend) []backendResponse {
	backends := make([]backendResponse, 0, len(held))
	for _, backend := range held {
		entry := backendResponse{
			InstanceID: backend.InstanceID,
			Address:    backend.Address,
			Healthy:    backend.Healthy,
			Running:    backend.Running,
			Probe:      backend.Probe,
			Reason:     backend.Reason,
			AddedAt:    backend.AddedAt.Format(time.RFC3339Nano),
		}
		if !backend.CheckedAt.IsZero() {
			entry.CheckedAt = backend.CheckedAt.Format(time.RFC3339Nano)
		}
		backends = append(backends, entry)
	}
	return backends
}

func toRoutes(held []Route) []routeResponse {
	if len(held) == 0 {
		return nil
	}

	routes := make([]routeResponse, 0, len(held))
	for _, one := range held {
		routes = append(routes, routeResponse{
			Host:     one.Host,
			Path:     one.Path,
			Service:  one.ServiceID,
			Backends: toBackends(one.Backends),
		})
	}
	return routes
}

func toResponse(b Balancer) response {
	backends := toBackends(b.Backends)

	var carried *tlsResponse
	if b.TLS.Present() {
		carried = &tlsResponse{Subject: b.TLS.Subject, ExpiresAt: b.TLS.ExpiresAt}
	}

	return response{
		TLS:        carried,
		ID:         b.ID,
		Name:       b.Name,
		Protocol:   b.Protocol,
		ListenPort: b.ListenPort,
		TargetPort: b.TargetPort,
		Algorithm:  b.Algorithm,
		Service:    b.ServiceID,
		Check:      b.Check,
		CheckPath:  b.CheckPath,
		Rise:       b.Rise,
		Fall:       b.Fall,
		Backends:   backends,
		Routes:     toRoutes(b.Routes),
		Family:     b.Family,
		CreatedAt:  b.CreatedAt.Format(time.RFC3339Nano),
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

	b, err := h.svc.create(r.Context(), CreateParams{
		ProjectID:  scope.From(r.Context()).ProjectID,
		Name:       req.Name,
		Protocol:   req.Protocol,
		ListenPort: req.ListenPort,
		TargetPort: req.TargetPort,
		Algorithm:  req.Algorithm,
		ServiceID:  req.Service,
		Check:      req.Check,
		CheckPath:  req.CheckPath,
		Rise:       req.Rise,
		Fall:       req.Fall,
		Family:     req.Family,
		Instances:  req.Instances,
		Routes:     toRouteParams(req.Routes),
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(b))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	balancers, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	writeList(w, balancers)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	b, err := h.svc.get(r.Context(), scope.From(r.Context()).ProjectID, r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	balancers, err := h.svc.forNode(r.Context())
	if err != nil {
		return err
	}

	body := nodeListResponse{Balancers: make([]nodeResponse, 0, len(balancers))}
	for _, b := range balancers {
		certPEM, keyPEM, err := h.svc.certificateOf(b)
		if err != nil {
			return err
		}
		body.Balancers = append(body.Balancers, nodeResponse{
			response:    toResponse(b),
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) setRoutes(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[routesRequest](w, r)
	if err != nil {
		return err
	}

	b, err := h.svc.setRoutes(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, toRouteParams(req.Routes))
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) setCertificate(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[certificateRequest](w, r)
	if err != nil {
		return err
	}

	b, err := h.svc.setCertificate(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, req.Certificate, req.PrivateKey)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) removeCertificate(w http.ResponseWriter, r *http.Request) error {
	b, err := h.svc.setCertificate(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, "", "")
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) reportHealth(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[healthRequest](w, r)
	if err != nil {
		return err
	}

	reports := make([]Report, 0, len(req.Checks))
	for _, entry := range req.Checks {
		reports = append(reports, Report{
			BalancerID: entry.BalancerID,
			InstanceID: entry.InstanceID,
			Healthy:    entry.Healthy,
			Reason:     entry.Reason,
		})
	}

	if err := h.svc.reportHealth(r.Context(), r.PathValue("nodeID"), reports); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func writeList(w http.ResponseWriter, balancers []Balancer) {
	body := listResponse{Balancers: make([]response, 0, len(balancers))}
	for _, b := range balancers {
		body.Balancers = append(body.Balancers, toResponse(b))
	}
	httpx.Write(w, http.StatusOK, body)
}

func (h *handler) addBackend(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[backendRequest](w, r)
	if err != nil {
		return err
	}

	b, err := h.svc.addBackend(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), req.InstanceID)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) removeBackend(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.removeBackend(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), r.PathValue("instanceID")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
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

func toRouteParams(asked []routeRequest) []RouteParams {
	if len(asked) == 0 {
		return nil
	}

	params := make([]RouteParams, 0, len(asked))
	for _, one := range asked {
		params = append(params, RouteParams{
			Host:    one.Host,
			Path:    one.Path,
			Service: one.Service,
		})
	}
	return params
}
