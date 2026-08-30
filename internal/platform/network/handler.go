package network

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
)

type createRequest struct {
	Name  string `json:"name"`
	CIDR  string `json:"cidr,omitempty"`
	CIDR6 string `json:"cidr6,omitempty"`
}

type response struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"`
	CIDR6     string `json:"cidr6,omitempty"`
	Gateway6  string `json:"gateway6,omitempty"`
	Gateway   string `json:"gateway"`
	Bridge    string `json:"bridge"`
	CreatedAt string `json:"created_at"`
}

type listResponse struct {
	Networks []response `json:"networks"`
}

type nicResponse struct {
	InstanceID string `json:"instance_id"`
	Device     int    `json:"device"`
	IP         string `json:"ip"`
	IP6        string `json:"ip6,omitempty"`
	MAC        string `json:"mac"`
}

type peerResponse struct {
	NodeID string `json:"node_id"`
	Slice  string `json:"slice"`
}

type nodeNetworkResponse struct {
	NetworkID string         `json:"network_id"`
	Name      string         `json:"name"`
	Bridge    string         `json:"bridge"`
	CIDR      string         `json:"cidr"`
	Gateway   string         `json:"gateway"`
	CIDR6     string         `json:"cidr6,omitempty"`
	Gateway6  string         `json:"gateway6,omitempty"`
	Slice     string         `json:"slice"`
	Slice6    string         `json:"slice6,omitempty"`
	NICs      []nicResponse  `json:"nics"`
	Peers     []peerResponse `json:"peers"`
}

type nodeViewResponse struct {
	Networks []nodeNetworkResponse `json:"networks"`
}

func toResponse(n Network) response {
	return response{
		ID:        n.ID,
		Name:      n.Name,
		CIDR:      n.CIDR,
		CIDR6:     n.CIDR6,
		Gateway6:  n.Gateway6,
		Gateway:   n.Gateway,
		Bridge:    n.Bridge,
		CreatedAt: n.CreatedAt.Format(time.RFC3339Nano),
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

	n, err := h.svc.create(r.Context(), CreateParams{
		ProjectID: scope.From(r.Context()).ProjectID,
		Name:      req.Name,
		CIDR:      req.CIDR,
		CIDR6:     req.CIDR6,
	})
	if err != nil {
		return err
	}

	httpx.Write(w, http.StatusCreated, toResponse(n))
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	networks, err := h.svc.listIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{Networks: make([]response, 0, len(networks))}
	for _, n := range networks {
		body.Networks = append(body.Networks, toResponse(n))
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) error {
	n, err := h.svc.getIn(r.Context(), r.PathValue("id"), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}
	httpx.Write(w, http.StatusOK, toResponse(n))
	return nil
}

func (h *handler) nodeView(w http.ResponseWriter, r *http.Request) error {
	views, err := h.svc.nodeView(r.Context(), r.PathValue("nodeID"))
	if err != nil {
		return err
	}

	body := nodeViewResponse{Networks: make([]nodeNetworkResponse, 0, len(views))}
	for _, v := range views {
		entry := nodeNetworkResponse{
			NetworkID: v.Network.ID,
			Name:      v.Network.Name,
			Bridge:    v.Network.Bridge,
			CIDR:      v.Network.CIDR,
			Gateway:   v.Network.Gateway,
			CIDR6:     v.Network.CIDR6,
			Gateway6:  v.Network.Gateway6,
			Slice:     v.Slice.CIDR,
			Slice6:    v.Slice.CIDR6,
			NICs:      make([]nicResponse, 0, len(v.NICs)),
			Peers:     make([]peerResponse, 0, len(v.Peers)),
		}
		for _, nic := range v.NICs {
			entry.NICs = append(entry.NICs, nicResponse{
				InstanceID: nic.InstanceID,
				Device:     nic.Device,
				IP:         nic.IP,
				IP6:        nic.IP6,
				MAC:        nic.MAC,
			})
		}
		for _, peer := range v.Peers {
			entry.Peers = append(entry.Peers, peerResponse{NodeID: peer.NodeID, Slice: peer.CIDR})
		}
		body.Networks = append(body.Networks, entry)
	}

	httpx.Write(w, http.StatusOK, body)
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
