package job

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name         string            `json:"name"`
	Isolation    string            `json:"isolation,omitempty"`
	Image        string            `json:"image,omitempty"`
	Kernel       string            `json:"kernel,omitempty"`
	Command      []string          `json:"command,omitempty"`
	NetworkID    string            `json:"network_id,omitempty"`
	FirewallID   string            `json:"firewall_id,omitempty"`
	VCPU         int               `json:"vcpu,omitempty"`
	MemoryMiB    int               `json:"memory_mib,omitempty"`
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Files        []File            `json:"files,omitempty"`
	Every        string            `json:"every,omitempty"`
	Retries      int               `json:"retries,omitempty"`
	Keep         int               `json:"keep,omitempty"`
}

type runResponse struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id,omitempty"`
	Attempt    int    `json:"attempt"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at,omitempty"`
}

type response struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Isolation    string            `json:"isolation"`
	Image        string            `json:"image,omitempty"`
	Kernel       string            `json:"kernel,omitempty"`
	Command      []string          `json:"command,omitempty"`
	NetworkID    string            `json:"network_id,omitempty"`
	FirewallID   string            `json:"firewall_id,omitempty"`
	VCPU         int               `json:"vcpu,omitempty"`
	MemoryMiB    int               `json:"memory_mib,omitempty"`
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	EnvNames     []string          `json:"env_names,omitempty"`
	FilePaths    []string          `json:"file_paths,omitempty"`
	Every        string            `json:"every,omitempty"`
	Retries      int               `json:"retries"`
	Keep         int               `json:"keep"`
	Paused       bool              `json:"paused,omitempty"`
	NextAt       string            `json:"next_at,omitempty"`
	LastAt       string            `json:"last_at,omitempty"`
	Runs         []runResponse     `json:"runs"`
	CreatedAt    string            `json:"created_at"`
	UpdatedAt    string            `json:"updated_at"`
}

type listResponse struct {
	Jobs []response `json:"jobs"`
}

type runsResponse struct {
	Runs []runResponse `json:"runs"`
}

func toRun(run Run) runResponse {
	body := runResponse{
		ID:         run.ID,
		InstanceID: run.InstanceID,
		Attempt:    run.Attempt,
		State:      run.State,
		Message:    run.Message,
		ExitCode:   run.ExitCode,
		StartedAt:  run.StartedAt.Format(time.RFC3339Nano),
	}
	if !run.EndedAt.IsZero() {
		body.EndedAt = run.EndedAt.Format(time.RFC3339Nano)
	}
	return body
}

func toResponse(j Job) response {
	runs := make([]runResponse, 0, len(j.Runs))
	for _, run := range j.Runs {
		runs = append(runs, toRun(run))
	}

	body := response{
		ID:           j.ID,
		Name:         j.Name,
		Isolation:    j.Template.Isolation,
		Image:        j.Template.Image,
		Kernel:       j.Template.Kernel,
		Command:      j.Template.Command,
		NetworkID:    j.Template.NetworkID,
		FirewallID:   j.Template.FirewallID,
		VCPU:         j.Template.VCPU,
		MemoryMiB:    j.Template.MemoryMiB,
		NodeSelector: j.Template.NodeSelector,
		EnvNames:     j.Template.EnvNames,
		FilePaths:    j.Template.FilePaths,
		Retries:      j.Retries,
		Keep:         j.Keep,
		Paused:       j.Paused,
		Runs:         runs,
		CreatedAt:    j.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:    j.UpdatedAt.Format(time.RFC3339Nano),
	}
	if j.Every > 0 {
		body.Every = j.Every.String()
	}
	if !j.NextAt.IsZero() {
		body.NextAt = j.NextAt.Format(time.RFC3339Nano)
	}
	if !j.LastAt.IsZero() {
		body.LastAt = j.LastAt.Format(time.RFC3339Nano)
	}
	return body
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
		Template: Template{
			Isolation:    req.Isolation,
			Image:        req.Image,
			Kernel:       req.Kernel,
			Command:      req.Command,
			NetworkID:    req.NetworkID,
			FirewallID:   req.FirewallID,
			VCPU:         req.VCPU,
			MemoryMiB:    req.MemoryMiB,
			NodeSelector: req.NodeSelector,
		},
		Env:     req.Env,
		Files:   req.Files,
		Every:   req.Every,
		Retries: req.Retries,
		Keep:    req.Keep,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(created))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	jobs, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Jobs: make([]response, 0, len(jobs))}
	for _, j := range jobs {
		body.Jobs = append(body.Jobs, toResponse(j))
	}
	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	found, err := h.svc.find(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(found))
	return nil
}

func (h *handler) runs(w http.ResponseWriter, r *http.Request) error {
	found, err := h.svc.find(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"))
	if err != nil {
		return err
	}

	body := runsResponse{Runs: make([]runResponse, 0, len(found.Runs))}
	for _, run := range found.Runs {
		body.Runs = append(body.Runs, toRun(run))
	}
	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) trigger(w http.ResponseWriter, r *http.Request) error {
	run, err := h.svc.trigger(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusCreated, toRun(run))
	return nil
}

func (h *handler) pause(w http.ResponseWriter, r *http.Request) error {
	return h.setPaused(w, r, true)
}

func (h *handler) resume(w http.ResponseWriter, r *http.Request) error {
	return h.setPaused(w, r, false)
}

func (h *handler) setPaused(w http.ResponseWriter, r *http.Request, paused bool) error {
	changed, err := h.svc.setPaused(r.Context(), scope.From(r.Context()).ProjectID,
		r.PathValue("id"), paused)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(changed))
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
