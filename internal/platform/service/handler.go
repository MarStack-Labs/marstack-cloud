package service

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name          string            `json:"name"`
	Replicas      int               `json:"replicas"`
	Isolation     string            `json:"isolation,omitempty"`
	Image         string            `json:"image,omitempty"`
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
	ExtraNetworks []string          `json:"extra_networks,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Files         []File            `json:"files,omitempty"`
	Keys          []string          `json:"keys,omitempty"`
}

type scaleRequest struct {
	Replicas int `json:"replicas"`
}

type updateRequest struct {
	Isolation     string            `json:"isolation,omitempty"`
	Image         string            `json:"image,omitempty"`
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
	ExtraNetworks []string          `json:"extra_networks,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Files         []File            `json:"files,omitempty"`
	Keys          []string          `json:"keys,omitempty"`
}

type memberResponse struct {
	InstanceID string `json:"instance_id"`
	Revision   int    `json:"revision"`
	CreatedAt  string `json:"created_at"`
}

type rolloutResponse struct {
	Current int `json:"current"`
	Stale   int `json:"stale"`
}

type response struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Replicas      int               `json:"replicas"`
	Revision      int               `json:"revision"`
	Isolation     string            `json:"isolation"`
	Image         string            `json:"image,omitempty"`
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
	ExtraNetworks []string          `json:"extra_networks,omitempty"`
	EnvNames      []string          `json:"env_names,omitempty"`
	FilePaths     []string          `json:"file_paths,omitempty"`
	Keys          []string          `json:"keys,omitempty"`
	Blocked       string            `json:"blocked,omitempty"`
	Rollout       rolloutResponse   `json:"rollout"`
	Members       []memberResponse  `json:"members"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
}

type listResponse struct {
	Services []response `json:"services"`
}

func toResponse(s Service) response {
	members := make([]memberResponse, 0, len(s.Members))
	rollout := rolloutResponse{}
	for _, member := range s.Members {
		members = append(members, memberResponse{
			InstanceID: member.InstanceID,
			Revision:   member.Revision,
			CreatedAt:  member.CreatedAt.Format(time.RFC3339Nano),
		})
		if member.Revision == s.Revision {
			rollout.Current++
			continue
		}
		rollout.Stale++
	}

	return response{
		ID:            s.ID,
		Name:          s.Name,
		Replicas:      s.Replicas,
		Revision:      s.Revision,
		Isolation:     s.Template.Isolation,
		Image:         s.Template.Image,
		ISO:           s.Template.ISO,
		Kernel:        s.Template.Kernel,
		DiskGiB:       s.Template.DiskGiB,
		FirewallID:    s.Template.FirewallID,
		Command:       s.Template.Command,
		NetworkID:     s.Template.NetworkID,
		RestartPolicy: s.Template.RestartPolicy,
		VCPU:          s.Template.VCPU,
		MemoryMiB:     s.Template.MemoryMiB,
		Group:         s.Template.Group,
		Strict:        s.Template.Strict,
		NodeSelector:  s.Template.NodeSelector,
		ExtraNetworks: s.Template.ExtraNetworks,
		EnvNames:      s.Template.EnvNames,
		FilePaths:     s.Template.FilePaths,
		Keys:          s.Template.Keys,
		Blocked:       s.Blocked,
		Rollout:       rollout,
		Members:       members,
		CreatedAt:     s.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:     s.UpdatedAt.Format(time.RFC3339Nano),
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
		ProjectID: scope.From(r.Context()).ProjectID,
		Name:      req.Name,
		Replicas:  req.Replicas,
		Template: Template{
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
			Group:         req.Group,
			NodeSelector:  req.NodeSelector,
			ExtraNetworks: req.ExtraNetworks,
			Env:           req.Env,
			Files:         req.Files,
			Strict:        req.Strict,
			Keys:          req.Keys,
		},
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(created))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	services, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Services: make([]response, 0, len(services))}
	for _, s := range services {
		body.Services = append(body.Services, toResponse(s))
	}
	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	found, err := h.svc.find(r.Context(), scope.From(r.Context()).ProjectID, r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(found))
	return nil
}

func (h *handler) scale(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[scaleRequest](w, r)
	if err != nil {
		return err
	}

	scaled, err := h.svc.scale(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), req.Replicas)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(scaled))
	return nil
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[updateRequest](w, r)
	if err != nil {
		return err
	}

	updated, err := h.svc.update(r.Context(), UpdateParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		ID:        r.PathValue("id"),
		Template: Template{
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
			Group:         req.Group,
			NodeSelector:  req.NodeSelector,
			ExtraNetworks: req.ExtraNetworks,
			Env:           req.Env,
			Files:         req.Files,
			Strict:        req.Strict,
			Keys:          req.Keys,
		},
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(updated))
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
