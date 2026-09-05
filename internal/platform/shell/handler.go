package shell

import (
	"io"
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type startRequest struct {
	Command []string `json:"command,omitempty"`
}

type response struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id"`
	NodeID     string   `json:"node_id,omitempty"`
	Isolation  string   `json:"isolation,omitempty"`
	Command    []string `json:"command"`
	State      string   `json:"state"`
	Message    string   `json:"message,omitempty"`
	CreatedAt  string   `json:"created_at"`
}

type listResponse struct {
	Sessions []response `json:"sessions"`
}

func toResponse(s Session) response {
	return response{
		ID:         s.ID,
		InstanceID: s.InstanceID,
		NodeID:     s.NodeID,
		Isolation:  s.Isolation,
		Command:    s.Command,
		State:      s.State,
		Message:    s.Message,
		CreatedAt:  s.CreatedAt.Format(time.RFC3339Nano),
	}
}

type handler struct {
	svc *service
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[startRequest](w, r)
	if err != nil {
		return err
	}

	held, err := h.svc.start(r.Context(), StartParams{
		ProjectID:   scope.From(r.Context()).ProjectID,
		InstanceRef: r.PathValue("id"),
		Command:     req.Command,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(held))
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.readIn(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusOK, toResponse(held))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID, 20)
	if err != nil {
		return err
	}

	body := listResponse{Sessions: make([]response, 0, len(held))}
	for _, one := range held {
		body.Sessions = append(body.Sessions, toResponse(one))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) closeSession(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.readIn(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	if err := h.svc.close(r.Context(), held.ID, "closed by the operator"); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) clientOutput(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.readIn(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	channel, open := h.svc.relay.find(held.ID)
	if !open {
		return closedAlready(w)
	}

	stream(w, r, channel.toClient)
	_ = h.svc.close(r.Context(), held.ID, "the operator left")
	return nil
}

func (h *handler) clientInput(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.readIn(r.Context(), r.PathValue("id"),
		scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	channel, open := h.svc.relay.find(held.ID)
	if !open {
		return closedAlready(w)
	}

	if _, err := io.Copy(channel.toNodeIn, io.LimitReader(r.Body, PipeBytes)); err != nil {
		return nil
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) take(w http.ResponseWriter, r *http.Request) error {
	held, found, err := h.svc.take(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	httpx.Write(w, http.StatusOK, toResponse(held))
	return nil
}

func (h *handler) nodeOutput(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.heldBy(r.Context(), r.PathValue("execID"), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	channel, open := h.svc.relay.find(held.ID)
	if !open {
		return closedAlready(w)
	}

	_, copyErr := io.Copy(channel.toClientIn, r.Body)
	reason := "the shell ended"
	if copyErr != nil {
		reason = "the shell ended: " + copyErr.Error()
	}
	_ = h.svc.close(r.Context(), held.ID, reason)

	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) nodeInput(w http.ResponseWriter, r *http.Request) error {
	held, err := h.svc.heldBy(r.Context(), r.PathValue("execID"), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	channel, open := h.svc.relay.find(held.ID)
	if !open {
		return closedAlready(w)
	}

	stream(w, r, channel.toNode)
	_ = h.svc.close(r.Context(), held.ID, "the node stopped reading")
	return nil
}

func stream(w http.ResponseWriter, r *http.Request, source *io.PipeReader) {
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-r.Context().Done():
			source.CloseWithError(r.Context().Err())
		case <-done:
		}
	}()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	control := http.NewResponseController(w)
	_ = control.Flush()

	buffer := make([]byte, 4096)
	for {
		read, err := source.Read(buffer)
		if read > 0 {
			if _, writeErr := w.Write(buffer[:read]); writeErr != nil {
				return
			}
			if flushErr := control.Flush(); flushErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
		if r.Context().Err() != nil {
			return
		}
	}
}

func closedAlready(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusGone)
	_, _ = w.Write([]byte("this shell session is closed"))
	return nil
}
