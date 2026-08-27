package quota

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type limitsBody struct {
	Instances int `json:"instances"`
	VCPU      int `json:"vcpu"`
	MemoryMiB int `json:"memory_mib"`
	Volumes   int `json:"volumes"`
	VolumeGiB int `json:"volume_gib"`
}

type setRequest struct {
	Instances int `json:"instances,omitempty"`
	VCPU      int `json:"vcpu,omitempty"`
	MemoryMiB int `json:"memory_mib,omitempty"`
	Volumes   int `json:"volumes,omitempty"`
	VolumeGiB int `json:"volume_gib,omitempty"`
}

type response struct {
	ProjectID string     `json:"project_id"`
	Limits    limitsBody `json:"limits"`
	Used      limitsBody `json:"used"`
	UpdatedAt string     `json:"updated_at,omitempty"`
}

type listResponse struct {
	Quotas []response `json:"quotas"`
}

func toResponse(r Report) response {
	body := response{
		ProjectID: r.ProjectID,
		Limits: limitsBody{
			Instances: r.Limits.Instances,
			VCPU:      r.Limits.VCPU,
			MemoryMiB: r.Limits.MemoryMiB,
			Volumes:   r.Limits.Volumes,
			VolumeGiB: r.Limits.VolumeGiB,
		},
		Used: limitsBody{
			Instances: r.Consumed.Instances,
			VCPU:      r.Consumed.VCPU,
			MemoryMiB: r.Consumed.MemoryMiB,
			Volumes:   r.Consumed.Volumes,
			VolumeGiB: r.Consumed.VolumeGiB,
		},
	}
	if !r.UpdatedAt.IsZero() {
		body.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	}
	return body
}

type handler struct {
	svc *service
}

func (h *handler) mine(w http.ResponseWriter, r *http.Request) error {
	report, err := h.svc.report(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(report))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	reports, err := h.svc.reports(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Quotas: make([]response, 0, len(reports))}
	for _, report := range reports {
		body.Quotas = append(body.Quotas, toResponse(report))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	report, err := h.svc.report(r.Context(), r.PathValue("projectID"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(report))
	return nil
}

func (h *handler) set(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[setRequest](w, r)
	if err != nil {
		return err
	}

	q, err := h.svc.set(r.Context(), r.PathValue("projectID"), Limits{
		Instances: req.Instances,
		VCPU:      req.VCPU,
		MemoryMiB: req.MemoryMiB,
		Volumes:   req.Volumes,
		VolumeGiB: req.VolumeGiB,
	})
	if err != nil {
		return err
	}

	report, err := h.svc.report(r.Context(), q.ProjectID)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(report))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("projectID")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
