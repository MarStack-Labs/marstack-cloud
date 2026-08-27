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
	ID        string `json:"id"`
	VolumeID  string `json:"volume_id"`
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	Checksum  string `json:"checksum,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type listResponse struct {
	Backups []response `json:"backups"`
}

func toResponse(b Backup) response {
	return response{
		ID:        b.ID,
		VolumeID:  b.VolumeID,
		NodeID:    b.NodeID,
		Name:      b.Name,
		State:     b.State,
		Message:   b.Message,
		SizeBytes: b.SizeBytes,
		Checksum:  b.Checksum,
		CreatedAt: b.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt: b.UpdatedAt.Format(time.RFC3339Nano),
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
