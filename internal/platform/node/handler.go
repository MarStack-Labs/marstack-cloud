package node

import (
	"net/http"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type registerRequest struct {
	Name         string `json:"name"`
	Zone         string `json:"zone,omitempty"`
	Address      string `json:"address,omitempty"`
	Arch         string `json:"arch"`
	OS           string `json:"os"`
	CPUs         int    `json:"cpus"`
	MemoryMiB    int    `json:"memory_mib"`
	AgentVersion string `json:"agent_version"`
}

type response struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Zone         string            `json:"zone,omitempty"`
	Address      string            `json:"address,omitempty"`
	Status       string            `json:"status"`
	Schedulable  bool              `json:"schedulable"`
	Draining     bool              `json:"draining,omitempty"`
	Arch         string            `json:"arch"`
	OS           string            `json:"os"`
	CPUs         int               `json:"cpus"`
	MemoryMiB    int               `json:"memory_mib"`
	AgentVersion string            `json:"agent_version"`
	Labels       map[string]string `json:"labels,omitempty"`
	RegisteredAt string            `json:"registered_at"`
	LastSeenAt   string            `json:"last_seen_at"`
}

type labelsRequest struct {
	Labels map[string]string `json:"labels"`
}

type listResponse struct {
	Nodes []response `json:"nodes"`
}

type handler struct {
	svc *service
}

func (h *handler) toResponse(n Node) response {
	return response{
		ID:           n.ID,
		Name:         n.Name,
		Zone:         n.Zone,
		Address:      n.Address,
		Status:       string(n.StatusAt(h.svc.now())),
		Schedulable:  n.Schedulable,
		Draining:     n.Draining,
		Arch:         n.Arch,
		OS:           n.OS,
		CPUs:         n.CPUs,
		MemoryMiB:    n.MemoryMiB,
		AgentVersion: n.AgentVersion,
		Labels:       n.Labels,
		RegisteredAt: n.RegisteredAt.Format(time.RFC3339Nano),
		LastSeenAt:   n.LastSeenAt.Format(time.RFC3339Nano),
	}
}

func (h *handler) register(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[registerRequest](w, r)
	if err != nil {
		return err
	}

	n, err := h.svc.register(r.Context(), RegisterParams{
		Name:         req.Name,
		Zone:         req.Zone,
		Address:      req.Address,
		Arch:         req.Arch,
		OS:           req.OS,
		CPUs:         req.CPUs,
		MemoryMiB:    req.MemoryMiB,
		AgentVersion: req.AgentVersion,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, h.toResponse(n))
	return nil
}

func (h *handler) heartbeat(w http.ResponseWriter, r *http.Request) error {
	n, err := h.svc.heartbeat(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, h.toResponse(n))
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	n, err := h.svc.get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, h.toResponse(n))
	return nil
}

func (h *handler) cordon(w http.ResponseWriter, r *http.Request) error {
	return h.setCordon(w, r, false)
}

func (h *handler) uncordon(w http.ResponseWriter, r *http.Request) error {
	return h.setCordon(w, r, true)
}

func (h *handler) setCordon(w http.ResponseWriter, r *http.Request, schedulable bool) error {
	n, err := h.svc.cordon(r.Context(), r.PathValue("id"), schedulable)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, h.toResponse(n))
	return nil
}

func (h *handler) drain(w http.ResponseWriter, r *http.Request) error {
	n, err := h.svc.drain(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, h.toResponse(n))
	return nil
}

func (h *handler) setLabels(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[labelsRequest](w, r)
	if err != nil {
		return err
	}

	n, err := h.svc.setLabels(r.Context(), r.PathValue("id"), req.Labels)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, h.toResponse(n))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	selector, err := selectorFrom(r)
	if err != nil {
		return err
	}

	nodes, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Nodes: make([]response, 0, len(nodes))}
	for _, n := range nodes {
		if !n.Matches(selector) {
			continue
		}
		body.Nodes = append(body.Nodes, h.toResponse(n))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func selectorFrom(r *http.Request) (map[string]string, error) {
	raw := r.URL.Query()["label"]
	if len(raw) == 0 {
		return nil, nil
	}

	selector := make(map[string]string, len(raw))
	for _, pair := range raw {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return nil, fault.Invalid("invalid_label",
				"a label filter is key=value, and "+pair+" has no value")
		}
		if err := validateLabels(map[string]string{key: value}); err != nil {
			return nil, err
		}
		selector[key] = value
	}
	return selector, nil
}

type deviceRequest struct {
	Devices []deviceEntry `json:"devices"`
}

type deviceEntry struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
	Vendor  string `json:"vendor,omitempty"`
	Product string `json:"product,omitempty"`
	Driver  string `json:"driver,omitempty"`
	Ready   bool   `json:"ready,omitempty"`
}

type deviceResponse struct {
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	Kind       string `json:"kind"`
	Vendor     string `json:"vendor,omitempty"`
	Product    string `json:"product,omitempty"`
	Driver     string `json:"driver,omitempty"`
	Ready      bool   `json:"ready"`
	InstanceID string `json:"instance_id,omitempty"`
}

type deviceListResponse struct {
	Devices []deviceResponse `json:"devices"`
}

func (h *handler) setDevices(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[deviceRequest](w, r)
	if err != nil {
		return err
	}

	held := make([]Device, 0, len(req.Devices))
	for _, one := range req.Devices {
		held = append(held, Device{
			Address: one.Address,
			Kind:    one.Kind,
			Vendor:  one.Vendor,
			Product: one.Product,
			Driver:  one.Driver,
			Ready:   one.Ready,
		})
	}

	if err := h.svc.setDevices(r.Context(), r.PathValue("id"), held); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) listDevices(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.devices(r.Context())
	if err != nil {
		return err
	}

	body := deviceListResponse{Devices: make([]deviceResponse, 0, len(held))}
	for _, one := range held {
		body.Devices = append(body.Devices, deviceResponse{
			NodeID:     one.NodeID,
			Address:    one.Address,
			Kind:       one.Kind,
			Vendor:     one.Vendor,
			Product:    one.Product,
			Driver:     one.Driver,
			Ready:      one.Ready,
			InstanceID: one.InstanceID,
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
