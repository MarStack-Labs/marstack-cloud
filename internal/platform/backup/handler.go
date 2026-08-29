package backup

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name string `json:"name"`
}

type failRequest struct {
	Message string `json:"message"`
}

type response struct {
	ID         string `json:"id"`
	VolumeID   string `json:"volume_id"`
	NodeID     string `json:"node_id"`
	ScheduleID string `json:"schedule_id,omitempty"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	Checksum   string `json:"checksum,omitempty"`
	KeyID      string `json:"key_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type listResponse struct {
	Backups []response `json:"backups"`
}

func toResponse(b Backup) response {
	return response{
		ID:         b.ID,
		VolumeID:   b.VolumeID,
		NodeID:     b.NodeID,
		ScheduleID: b.ScheduleID,
		Name:       b.Name,
		State:      b.State,
		Message:    b.Message,
		SizeBytes:  b.SizeBytes,
		Checksum:   b.Checksum,
		KeyID:      b.KeyID,
		CreatedAt:  b.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:  b.UpdatedAt.Format(time.RFC3339Nano),
	}
}

type handler struct {
	svc *service
}

func unbounded(w http.ResponseWriter) {
	control := http.NewResponseController(w)
	_ = control.SetReadDeadline(time.Time{})
	_ = control.SetWriteDeadline(time.Time{})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[createRequest](w, r)
	if err != nil {
		return err
	}

	b, err := h.svc.create(r.Context(), CreateParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		VolumeID:  r.PathValue("id"),
		Name:      req.Name,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(b))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	backups, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	writeList(w, backups)
	return nil
}

func (h *handler) listForVolume(w http.ResponseWriter, r *http.Request) error {
	backups, err := h.svc.listForVolume(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	writeList(w, backups)
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	backups, err := h.svc.pendingOn(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}
	writeList(w, backups)
	return nil
}

func writeList(w http.ResponseWriter, backups []Backup) {
	body := listResponse{Backups: make([]response, 0, len(backups))}
	for _, b := range backups {
		body.Backups = append(body.Backups, toResponse(b))
	}
	httpx.Write(w, http.StatusOK, body)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	b, err := h.svc.getIn(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) upload(w http.ResponseWriter, r *http.Request) error {
	unbounded(w)

	b, err := h.svc.store(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), r.Body)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(b))
	return nil
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[failRequest](w, r)
	if err != nil {
		return err
	}
	if err := h.svc.fail(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), req.Message); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) download(w http.ResponseWriter, r *http.Request) error {
	unbounded(w)

	file, size, err := h.svc.content(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, file); err != nil {
		return fault.Internal(err)
	}
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

type scheduleRequest struct {
	Every string `json:"every"`
	Keep  int    `json:"keep"`
}

type scheduleResponse struct {
	ID        string `json:"id"`
	VolumeID  string `json:"volume_id"`
	Every     string `json:"every"`
	Keep      int    `json:"keep"`
	NextAt    string `json:"next_at"`
	LastAt    string `json:"last_at,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

type scheduleListResponse struct {
	Schedules []scheduleResponse `json:"schedules"`
}

func toScheduleResponse(sc Schedule) scheduleResponse {
	body := scheduleResponse{
		ID:        sc.ID,
		VolumeID:  sc.VolumeID,
		Every:     sc.Every.String(),
		Keep:      sc.Keep,
		NextAt:    sc.NextAt.Format(time.RFC3339Nano),
		UpdatedAt: sc.UpdatedAt.Format(time.RFC3339Nano),
	}
	if !sc.LastAt.IsZero() {
		body.LastAt = sc.LastAt.Format(time.RFC3339Nano)
	}
	return body
}

func (h *handler) setSchedule(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[scheduleRequest](w, r)
	if err != nil {
		return err
	}

	sc, err := h.svc.setSchedule(r.Context(), ScheduleParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		VolumeID:  r.PathValue("id"),
		Every:     req.Every,
		Keep:      req.Keep,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toScheduleResponse(sc))
	return nil
}

func (h *handler) getSchedule(w http.ResponseWriter, r *http.Request) error {
	sc, err := h.svc.scheduleOf(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toScheduleResponse(sc))
	return nil
}

func (h *handler) listSchedules(w http.ResponseWriter, r *http.Request) error {
	schedules, err := h.svc.schedulesIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := scheduleListResponse{Schedules: make([]scheduleResponse, 0, len(schedules))}
	for _, sc := range schedules {
		body.Schedules = append(body.Schedules, toScheduleResponse(sc))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) deleteSchedule(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.removeSchedule(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type transferResponse struct {
	Direct    bool   `json:"direct"`
	URL       string `json:"url,omitempty"`
	Key       string `json:"key,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type uploadedRequest struct {
	SizeBytes int64  `json:"size_bytes"`
	Checksum  string `json:"checksum"`
}

func (h *handler) uploadTarget(w http.ResponseWriter, r *http.Request) error {
	return h.transfer(w, r, http.MethodPut)
}

func (h *handler) downloadTarget(w http.ResponseWriter, r *http.Request) error {
	return h.transfer(w, r, http.MethodGet)
}

func (h *handler) transfer(w http.ResponseWriter, r *http.Request, method string) error {
	target, direct, err := h.svc.transfer(r.Context(), r.PathValue("id"),
		r.PathValue("nodeID"), method)
	if err != nil {
		return err
	}
	if !direct {
		httpx.Write(w, http.StatusOK, transferResponse{Direct: false})
		return nil
	}

	httpx.Write(w, http.StatusOK, transferResponse{
		Direct:    true,
		URL:       target.URL,
		Key:       target.Key,
		ExpiresAt: target.ExpiresAt.Format(time.RFC3339Nano),
	})
	return nil
}

func (h *handler) uploaded(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[uploadedRequest](w, r)
	if err != nil {
		return err
	}

	if err := h.svc.markUploaded(r.Context(), r.PathValue("id"), r.PathValue("nodeID"),
		req.SizeBytes, req.Checksum); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
