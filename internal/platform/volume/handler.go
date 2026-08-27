package volume

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name      string `json:"name"`
	SizeGiB   int    `json:"size_gib"`
	BackupID  string `json:"from_backup,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
}

type attachRequest struct {
	InstanceID string `json:"instance_id"`
}

type snapshotRequest struct {
	Name string `json:"name"`
}

type reportRequest struct {
	Volumes []reportedVolume `json:"volumes"`
}

type reportedVolume struct {
	VolumeID  string             `json:"volume_id"`
	Snapshots []reportedSnapshot `json:"snapshots"`
	Restored  string             `json:"restored,omitempty"`
	Error     string             `json:"error,omitempty"`
}

type reportedSnapshot struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type snapshotResponse struct {
	ID        string `json:"id"`
	VolumeID  string `json:"volume_id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	CreatedAt string `json:"created_at"`
}

type snapshotListResponse struct {
	Snapshots []snapshotResponse `json:"snapshots"`
}

func toSnapshotResponse(snap Snapshot) snapshotResponse {
	return snapshotResponse{
		ID:        snap.ID,
		VolumeID:  snap.VolumeID,
		Name:      snap.Name,
		State:     snap.State,
		Message:   snap.Message,
		SizeBytes: snap.SizeBytes,
		CreatedAt: snap.CreatedAt.Format(time.RFC3339Nano),
	}
}

type response struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	SizeGiB     int                `json:"size_gib"`
	State       string             `json:"state"`
	NodeID      string             `json:"node_id,omitempty"`
	InstanceID  string             `json:"instance_id,omitempty"`
	RestoreFrom string             `json:"restore_from,omitempty"`
	BackupID    string             `json:"backup_id,omitempty"`
	Encrypted   bool               `json:"encrypted,omitempty"`
	KeyID       string             `json:"key_id,omitempty"`
	Snapshots   []snapshotResponse `json:"snapshots,omitempty"`
	CreatedAt   string             `json:"created_at"`
	UpdatedAt   string             `json:"updated_at"`
}

type listResponse struct {
	Volumes []response `json:"volumes"`
}

func toResponse(v Volume) response {
	return response{
		ID:          v.ID,
		Name:        v.Name,
		SizeGiB:     v.SizeGiB,
		State:       v.State(),
		NodeID:      v.NodeID,
		InstanceID:  v.InstanceID,
		RestoreFrom: v.RestoreFrom,
		BackupID:    v.BackupID,
		Encrypted:   v.Encrypted,
		KeyID:       v.KeyID,
		CreatedAt:   v.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:   v.UpdatedAt.Format(time.RFC3339Nano),
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

	v, err := h.svc.create(r.Context(), CreateParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		Name:      req.Name,
		SizeGiB:   req.SizeGiB,
		BackupID:  req.BackupID,
		Encrypted: req.Encrypted,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(v))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	volumes, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Volumes: make([]response, 0, len(volumes))}
	for _, v := range volumes {
		body.Volumes = append(body.Volumes, toResponse(v))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.resolveIn(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(v))
	return nil
}

func (h *handler) attach(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[attachRequest](w, r)
	if err != nil {
		return err
	}

	v, err := h.svc.attach(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, req.InstanceID)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(v))
	return nil
}

func (h *handler) detach(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.detach(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(v))
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

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	volumes, err := h.svc.onNode(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	body := listResponse{Volumes: make([]response, 0, len(volumes))}
	for _, v := range volumes {
		one := toResponse(v)

		snapshots, err := h.svc.snapshots(r.Context(), v.ID)
		if err != nil {
			return err
		}
		for _, snap := range snapshots {
			one.Snapshots = append(one.Snapshots, toSnapshotResponse(snap))
		}

		body.Volumes = append(body.Volumes, one)
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) snapshot(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[snapshotRequest](w, r)
	if err != nil {
		return err
	}

	snap, err := h.svc.snapshot(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID, req.Name)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toSnapshotResponse(snap))
	return nil
}

func (h *handler) listSnapshots(w http.ResponseWriter, r *http.Request) error {
	project := scope.From(r.Context()).ProjectID

	var (
		snapshots []Snapshot
		err       error
	)
	if key := r.PathValue("id"); key != "" {
		v, resolveErr := h.svc.resolveIn(r.Context(), key, project)
		if resolveErr != nil {
			return resolveErr
		}
		snapshots, err = h.svc.snapshots(r.Context(), v.ID)
	} else {
		snapshots, err = h.svc.snapshotsIn(r.Context(), project)
	}
	if err != nil {
		return err
	}

	body := snapshotListResponse{Snapshots: make([]snapshotResponse, 0, len(snapshots))}
	for _, snap := range snapshots {
		body.Snapshots = append(body.Snapshots, toSnapshotResponse(snap))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) deleteSnapshot(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.removeSnapshot(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) restore(w http.ResponseWriter, r *http.Request) error {
	v, err := h.svc.restore(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(v))
	return nil
}

func (h *handler) report(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[reportRequest](w, r)
	if err != nil {
		return err
	}

	reports := make([]NodeReport, 0, len(req.Volumes))
	for _, reported := range req.Volumes {
		snapshots := make([]ReportedSnapshot, 0, len(reported.Snapshots))
		for _, snap := range reported.Snapshots {
			snapshots = append(snapshots, ReportedSnapshot{Name: snap.Name, SizeBytes: snap.SizeBytes})
		}
		reports = append(reports, NodeReport{
			VolumeID:  reported.VolumeID,
			Snapshots: snapshots,
			Restored:  reported.Restored,
			Error:     reported.Error,
		})
	}

	if err := h.svc.report(r.Context(), r.PathValue("nodeID"), reports); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

type keyResponse struct {
	VolumeID string `json:"volume_id"`
	Key      string `json:"key"`
}

func (h *handler) nodeKey(w http.ResponseWriter, r *http.Request) error {
	key, err := h.svc.keyForNode(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, keyResponse{VolumeID: r.PathValue("id"), Key: key})
	return nil
}
