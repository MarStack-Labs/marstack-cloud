package instance

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type createRequest struct {
	Name          string   `json:"name"`
	Isolation     string   `json:"isolation"`
	Image         string   `json:"image"`
	ISO           string   `json:"iso,omitempty"`
	Kernel        string   `json:"kernel,omitempty"`
	DiskGiB       int      `json:"disk_gib,omitempty"`
	FirewallID    string   `json:"firewall_id,omitempty"`
	Command       []string `json:"command,omitempty"`
	NetworkID     string   `json:"network_id,omitempty"`
	RestartPolicy string   `json:"restart_policy,omitempty"`
	VCPU          int      `json:"vcpu,omitempty"`
	MemoryMiB     int      `json:"memory_mib,omitempty"`
}

type statusRequest struct {
	ObservedState string `json:"observed_state"`
	Message       string `json:"message,omitempty"`
	Restarts      int    `json:"restarts,omitempty"`
}

type response struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Isolation       string   `json:"isolation"`
	Image           string   `json:"image,omitempty"`
	ISO             string   `json:"iso,omitempty"`
	Kernel          string   `json:"kernel,omitempty"`
	DiskGiB         int      `json:"disk_gib,omitempty"`
	FirewallID      string   `json:"firewall_id,omitempty"`
	Command         []string `json:"command,omitempty"`
	NetworkID       string   `json:"network_id,omitempty"`
	RestartPolicy   string   `json:"restart_policy"`
	RestartCount    int      `json:"restart_count"`
	VCPU            int      `json:"vcpu"`
	MemoryMiB       int      `json:"memory_mib"`
	Desired         string   `json:"desired_state"`
	Observed        string   `json:"observed_state"`
	ObservedMessage string   `json:"observed_message,omitempty"`
	NodeID          string   `json:"node_id,omitempty"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

type listResponse struct {
	Instances []response `json:"instances"`
}

func toResponse(in Instance) response {
	return response{
		ID:              in.ID,
		Name:            in.Name,
		Isolation:       string(in.Isolation),
		Image:           in.Image,
		ISO:             in.ISO,
		Kernel:          in.Kernel,
		DiskGiB:         in.DiskGiB,
		FirewallID:      in.FirewallID,
		Command:         in.Command,
		NetworkID:       in.NetworkID,
		RestartPolicy:   string(in.RestartPolicy),
		RestartCount:    in.RestartCount,
		VCPU:            in.VCPU,
		MemoryMiB:       in.MemoryMiB,
		Desired:         string(in.Desired),
		Observed:        string(in.Observed),
		ObservedMessage: in.ObservedMessage,
		NodeID:          in.NodeID,
		CreatedAt:       in.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:       in.UpdatedAt.Format(time.RFC3339Nano),
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

	in, err := h.svc.create(r.Context(), CreateParams{
		Name:          req.Name,
		Isolation:     req.Isolation,
		Image:         req.Image,
		ISO:           req.ISO,
		Kernel:        req.Kernel,
		DiskGiB:       req.DiskGiB,
		FirewallID:    req.FirewallID,
		Command:       req.Command,
		NetworkID:     req.NetworkID,
		RestartPolicy: req.RestartPolicy,
		VCPU:          req.VCPU,
		MemoryMiB:     req.MemoryMiB,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(in))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	instances, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Instances: make([]response, 0, len(instances))}
	for _, in := range instances {
		body.Instances = append(body.Instances, toResponse(in))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	in, err := h.svc.get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(in))
	return nil
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) error {
	return h.transition(w, r, DesiredRunning)
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) error {
	return h.transition(w, r, DesiredStopped)
}

func (h *handler) transition(w http.ResponseWriter, r *http.Request, desired DesiredState) error {
	in, err := h.svc.setDesired(r.Context(), r.PathValue("id"), desired)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusAccepted, toResponse(in))
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	instances, err := h.svc.listByNode(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	body := listResponse{Instances: make([]response, 0, len(instances))}
	for _, in := range instances {
		body.Instances = append(body.Instances, toResponse(in))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) reportStatus(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[statusRequest](w, r)
	if err != nil {
		return err
	}

	in, err := h.svc.reportObserved(
		r.Context(),
		r.PathValue("nodeID"),
		r.PathValue("instanceID"),
		req.ObservedState,
		req.Message,
		req.Restarts,
	)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(in))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.delete(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	httpx.Write(w, http.StatusNoContent, nil)
	return nil
}
