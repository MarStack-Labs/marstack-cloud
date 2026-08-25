package node

import (
	"net/http"
	"time"

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
	ID           string `json:"id"`
	Name         string `json:"name"`
	Zone         string `json:"zone,omitempty"`
	Address      string `json:"address,omitempty"`
	Status       string `json:"status"`
	Arch         string `json:"arch"`
	OS           string `json:"os"`
	CPUs         int    `json:"cpus"`
	MemoryMiB    int    `json:"memory_mib"`
	AgentVersion string `json:"agent_version"`
	RegisteredAt string `json:"registered_at"`
	LastSeenAt   string `json:"last_seen_at"`
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
		Arch:         n.Arch,
		OS:           n.OS,
		CPUs:         n.CPUs,
		MemoryMiB:    n.MemoryMiB,
		AgentVersion: n.AgentVersion,
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

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	nodes, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Nodes: make([]response, 0, len(nodes))}
	for _, n := range nodes {
		body.Nodes = append(body.Nodes, h.toResponse(n))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}
