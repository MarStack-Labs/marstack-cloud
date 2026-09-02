package instance

import (
	"io"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/page"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name          string            `json:"name"`
	Isolation     string            `json:"isolation"`
	Image         string            `json:"image"`
	ISO           string            `json:"iso,omitempty"`
	Kernel        string            `json:"kernel,omitempty"`
	DiskGiB       int               `json:"disk_gib,omitempty"`
	FirewallID    string            `json:"firewall_id,omitempty"`
	Command       []string          `json:"command,omitempty"`
	NetworkID     string            `json:"network_id,omitempty"`
	RestartPolicy string            `json:"restart_policy,omitempty"`
	VCPU          int               `json:"vcpu,omitempty"`
	MemoryMiB     int               `json:"memory_mib,omitempty"`
	Group         string            `json:"placement_group,omitempty"`
	Strict        bool              `json:"placement_strict,omitempty"`
	NodeSelector  map[string]string `json:"node_selector,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Files         []File            `json:"files,omitempty"`
	ExtraNetworks []string          `json:"extra_networks,omitempty"`
	Keys          []string          `json:"keys,omitempty"`
}

type resizeRequest struct {
	VCPU      int `json:"vcpu,omitempty"`
	MemoryMiB int `json:"memory_mib,omitempty"`
}

type statusRequest struct {
	ObservedState string `json:"observed_state"`
	Message       string `json:"message,omitempty"`
	Restarts      int    `json:"restarts,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
}

type response struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Isolation       string            `json:"isolation"`
	Image           string            `json:"image,omitempty"`
	ISO             string            `json:"iso,omitempty"`
	Kernel          string            `json:"kernel,omitempty"`
	DiskGiB         int               `json:"disk_gib,omitempty"`
	FirewallID      string            `json:"firewall_id,omitempty"`
	Command         []string          `json:"command,omitempty"`
	NetworkID       string            `json:"network_id,omitempty"`
	RestartPolicy   string            `json:"restart_policy"`
	RestartCount    int               `json:"restart_count"`
	VCPU            int               `json:"vcpu"`
	MemoryMiB       int               `json:"memory_mib"`
	Group           string            `json:"placement_group,omitempty"`
	Strict          bool              `json:"placement_strict,omitempty"`
	NodeSelector    map[string]string `json:"node_selector,omitempty"`
	EnvNames        []string          `json:"env_names,omitempty"`
	FilePaths       []string          `json:"file_paths,omitempty"`
	ExtraNetworks   []string          `json:"extra_networks,omitempty"`
	SSHKeys         []string          `json:"ssh_keys,omitempty"`
	Desired         string            `json:"desired_state"`
	Observed        string            `json:"observed_state"`
	ObservedMessage string            `json:"observed_message,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	Migrating       bool              `json:"migrating,omitempty"`
	DiskParked      bool              `json:"disk_parked,omitempty"`
	NodeID          string            `json:"node_id,omitempty"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
}

type listResponse struct {
	Instances []response `json:"instances"`
	Next      string     `json:"next,omitempty"`
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
		Group:           in.Group,
		Strict:          in.Strict,
		NodeSelector:    in.NodeSelector,
		EnvNames:        in.EnvNames,
		FilePaths:       in.FilePaths,
		ExtraNetworks:   in.ExtraNetworks,
		SSHKeys:         in.SSHKeys,
		Desired:         string(in.Desired),
		Observed:        string(in.Observed),
		ObservedMessage: in.ObservedMessage,
		ExitCode:        in.ExitCode,
		Migrating:       in.Migrating,
		DiskParked:      in.DiskParked,
		NodeID:          in.NodeID,
		CreatedAt:       in.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:       in.UpdatedAt.Format(time.RFC3339Nano),
	}
}

type nodeResponse struct {
	response
	Env   map[string]string `json:"env,omitempty"`
	Files []File            `json:"files,omitempty"`
}

type nodeListResponse struct {
	Instances []nodeResponse `json:"instances"`
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
		ProjectID:     scope.From(r.Context()).ProjectID,
		Group:         req.Group,
		Strict:        req.Strict,
		NodeSelector:  req.NodeSelector,
		Env:           req.Env,
		Files:         req.Files,
		ExtraNetworks: req.ExtraNetworks,
		Keys:          req.Keys,
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
	window, err := page.From(r, page.Default, page.Max)
	if err != nil {
		return fault.Invalid("invalid_page", err.Error())
	}

	instances, err := h.svc.pageIn(r.Context(), scope.From(r.Context()).ProjectID, window)
	if err != nil {
		return err
	}

	body := listResponse{Instances: make([]response, 0, len(instances))}
	for _, in := range instances {
		body.Instances = append(body.Instances, toResponse(in))
	}
	if len(instances) == window.Limit {
		last := instances[len(instances)-1]
		body.Next = page.Encode(last.CreatedAt.Format(time.RFC3339Nano), last.ID)
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) parkDisk(w http.ResponseWriter, r *http.Request) error {
	defer r.Body.Close()

	if err := h.svc.parkDisk(r.Context(), r.PathValue("nodeID"),
		r.PathValue("instanceID"), r.Body); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) takeDisk(w http.ResponseWriter, r *http.Request) error {
	file, err := h.svc.parkedDisk(r.Context(), r.PathValue("nodeID"),
		r.PathValue("instanceID"))
	if err != nil {
		return err
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
	return nil
}

func (h *handler) landed(w http.ResponseWriter, r *http.Request) error {
	in, err := h.svc.finishMigration(r.Context(), r.PathValue("nodeID"),
		r.PathValue("instanceID"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(in))
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	in, err := h.svc.getIn(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
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
	in, err := h.svc.setDesired(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, desired)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusAccepted, toResponse(in))
	return nil
}

func (h *handler) resize(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[resizeRequest](w, r)
	if err != nil {
		return err
	}

	in, err := h.svc.resize(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, req.VCPU, req.MemoryMiB)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(in))
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	instances, err := h.svc.listByNode(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	body := nodeListResponse{Instances: make([]nodeResponse, 0, len(instances))}
	for _, in := range instances {
		env, err := h.svc.envOf(in)
		if err != nil {
			return err
		}

		files, err := h.svc.filesOf(in)
		if err != nil {
			return err
		}

		body.Instances = append(body.Instances, nodeResponse{
			response: toResponse(in),
			Env:      env,
			Files:    files,
		})
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
		req.ExitCode,
	)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(in))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.delete(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID); err != nil {
		return err
	}
	httpx.Write(w, http.StatusNoContent, nil)
	return nil
}
