package image

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Arch     string `json:"arch,omitempty"`
	Source   string `json:"source"`
	Checksum string `json:"checksum,omitempty"`
}

type reportRequest struct {
	Images []reportedImage `json:"images"`
}

type reportedImage struct {
	ImageID   string `json:"image_id"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type response struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Arch      string   `json:"arch"`
	Source    string   `json:"source"`
	Checksum  string   `json:"checksum,omitempty"`
	SizeBytes int64    `json:"size_bytes,omitempty"`
	Nodes     []string `json:"nodes"`
	CreatedAt string   `json:"created_at"`
}

type listResponse struct {
	Images []response `json:"images"`
}

func toResponse(in Image) response {
	return toPlacementResponse(Placement{Image: in})
}

func toPlacementResponse(placed Placement) response {
	in := placed.Image
	nodes := placed.Nodes
	if nodes == nil {
		nodes = []string{}
	}

	return response{
		ID:        in.ID,
		Name:      in.Name,
		Kind:      in.Kind,
		Arch:      in.Arch,
		Source:    in.Source,
		Checksum:  in.Checksum,
		SizeBytes: in.SizeBytes,
		Nodes:     nodes,
		CreatedAt: in.CreatedAt.Format(time.RFC3339Nano),
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
		ProjectID: scope.From(r.Context()).ProjectID,
		Name:      req.Name,
		Kind:      req.Kind,
		Arch:      req.Arch,
		Source:    req.Source,
		Checksum:  req.Checksum,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(in))
	return nil
}

func (h *handler) listForNode(w http.ResponseWriter, r *http.Request) error {
	images, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Images: make([]response, 0, len(images))}
	for _, placed := range images {
		body.Images = append(body.Images, toPlacementResponse(placed))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	images, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Images: make([]response, 0, len(images))}
	for _, placed := range images {
		body.Images = append(body.Images, toPlacementResponse(placed))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	in, err := h.svc.resolveIn(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	sized, err := h.svc.withSize(r.Context(), in)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(sized))
	return nil
}

func (h *handler) report(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[reportRequest](w, r)
	if err != nil {
		return err
	}

	staged := make([]Staged, 0, len(req.Images))
	for _, file := range req.Images {
		staged = append(staged, Staged{ImageID: file.ImageID, SizeBytes: file.SizeBytes})
	}

	if err := h.svc.report(r.Context(), r.PathValue("nodeID"), staged); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
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
