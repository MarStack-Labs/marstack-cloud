package usage

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
)

type reportRequest struct {
	CPUPercent    float64          `json:"cpu_percent"`
	MemoryUsedMiB int              `json:"memory_used_mib"`
	MemoryMiB     int              `json:"memory_mib"`
	Instances     []instanceReport `json:"instances,omitempty"`
}

type instanceReport struct {
	InstanceID    string  `json:"instance_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMiB int     `json:"memory_used_mib"`
}

type nodeResponse struct {
	NodeID        string  `json:"node_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMiB int     `json:"memory_used_mib"`
	MemoryMiB     int     `json:"memory_mib"`
	ReportedAt    string  `json:"reported_at"`
}

type instanceResponse struct {
	InstanceID    string  `json:"instance_id"`
	NodeID        string  `json:"node_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMiB int     `json:"memory_used_mib"`
	ReportedAt    string  `json:"reported_at"`
}

type listResponse struct {
	Nodes     []nodeResponse     `json:"nodes"`
	Instances []instanceResponse `json:"instances"`
}

type handler struct {
	svc *service
}

func (h *handler) report(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[reportRequest](w, r)
	if err != nil {
		return err
	}

	samples := make([]InstanceSample, 0, len(req.Instances))
	for _, sample := range req.Instances {
		samples = append(samples, InstanceSample{
			InstanceID:    sample.InstanceID,
			CPUPercent:    sample.CPUPercent,
			MemoryUsedMiB: sample.MemoryUsedMiB,
		})
	}

	if err := h.svc.record(r.Context(), Report{
		Node: NodeSample{
			NodeID:        r.PathValue("nodeID"),
			CPUPercent:    req.CPUPercent,
			MemoryUsedMiB: req.MemoryUsedMiB,
			MemoryMiB:     req.MemoryMiB,
		},
		Instances: samples,
	}); err != nil {
		return err
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	nodes, err := h.svc.nodes(r.Context())
	if err != nil {
		return err
	}

	instances, err := h.svc.instances(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{
		Nodes:     make([]nodeResponse, 0, len(nodes)),
		Instances: make([]instanceResponse, 0, len(instances)),
	}
	for _, sample := range nodes {
		body.Nodes = append(body.Nodes, nodeResponse{
			NodeID:        sample.NodeID,
			CPUPercent:    round(sample.CPUPercent),
			MemoryUsedMiB: sample.MemoryUsedMiB,
			MemoryMiB:     sample.MemoryMiB,
			ReportedAt:    sample.ReportedAt.Format(time.RFC3339Nano),
		})
	}
	for _, sample := range instances {
		body.Instances = append(body.Instances, instanceResponse{
			InstanceID:    sample.InstanceID,
			NodeID:        sample.NodeID,
			CPUPercent:    round(sample.CPUPercent),
			MemoryUsedMiB: sample.MemoryUsedMiB,
			ReportedAt:    sample.ReportedAt.Format(time.RFC3339Nano),
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func round(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}
