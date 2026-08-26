package image

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type createRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Arch     string `json:"arch,omitempty"`
	Source   string `json:"source"`
	Checksum string `json:"checksum,omitempty"`
}

type response struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Arch      string `json:"arch"`
	Source    string `json:"source"`
	Checksum  string `json:"checksum,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	CreatedAt string `json:"created_at"`
}

type listResponse struct {
	Images []response `json:"images"`
}

func toResponse(in Image) response {
	return response{
		ID:        in.ID,
		Name:      in.Name,
		Kind:      in.Kind,
		Arch:      in.Arch,
		Source:    in.Source,
		Checksum:  in.Checksum,
		SizeBytes: in.SizeBytes,
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
		Name:     req.Name,
		Kind:     req.Kind,
		Arch:     req.Arch,
		Source:   req.Source,
		Checksum: req.Checksum,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(in))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	images, err := h.svc.list(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Images: make([]response, 0, len(images))}
	for _, in := range images {
		body.Images = append(body.Images, toResponse(in))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	in, err := h.svc.resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(in))
	return nil
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.remove(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
