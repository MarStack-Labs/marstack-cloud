package usage

import (
	"net/http"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/scope"
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

func (h *handler) listNodes(w http.ResponseWriter, r *http.Request) error {
	nodes, err := h.svc.nodes(r.Context())
	if err != nil {
		return err
	}

	body := listResponse{Nodes: make([]nodeResponse, 0, len(nodes)), Instances: []instanceResponse{}}
	for _, sample := range nodes {
		body.Nodes = append(body.Nodes, nodeResponse{
			NodeID:        sample.NodeID,
			CPUPercent:    round(sample.CPUPercent),
			MemoryUsedMiB: sample.MemoryUsedMiB,
			MemoryMiB:     sample.MemoryMiB,
			ReportedAt:    sample.ReportedAt.Format(time.RFC3339Nano),
		})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) error {
	instances, err := h.svc.instancesIn(r.Context(), scope.From(r.Context()).ProjectID)
	if err != nil {
		return err
	}

	body := listResponse{
		Nodes:     []nodeResponse{},
		Instances: make([]instanceResponse, 0, len(instances)),
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

type bucketResponse struct {
	At            string  `json:"at"`
	Samples       int     `json:"samples"`
	CPUAverage    float64 `json:"cpu_average"`
	CPUPeak       float64 `json:"cpu_peak"`
	MemoryAverage int     `json:"memory_average"`
	MemoryPeak    int     `json:"memory_peak"`
	MemoryMiB     int     `json:"memory_mib,omitempty"`
}

type historyResponse struct {
	Subject string           `json:"subject"`
	Window  string           `json:"window"`
	Buckets []bucketResponse `json:"buckets"`
}

func (h *handler) history(w http.ResponseWriter, r *http.Request) error {
	window, err := windowOf(r)
	if err != nil {
		return err
	}

	found, err := h.svc.instanceHistory(r.Context(), scope.From(r.Context()).ProjectID,
		r.URL.Query().Get("subject"), window)
	if err != nil {
		return err
	}

	writeHistory(w, found, window)
	return nil
}

func (h *handler) nodeHistory(w http.ResponseWriter, r *http.Request) error {
	window, err := windowOf(r)
	if err != nil {
		return err
	}

	found, err := h.svc.historyOf(r.Context(), r.URL.Query().Get("subject"), window)
	if err != nil {
		return err
	}

	writeHistory(w, found, window)
	return nil
}

func windowOf(r *http.Request) (time.Duration, error) {
	raw := r.URL.Query().Get("window")
	if raw == "" {
		return DefaultWindow, nil
	}

	window, err := interval.Parse(raw)
	if err != nil {
		return 0, fault.Invalid("invalid_window", err.Error())
	}
	return window, nil
}

func writeHistory(w http.ResponseWriter, found History, window time.Duration) {
	body := historyResponse{
		Subject: found.Subject,
		Window:  window.String(),
		Buckets: make([]bucketResponse, 0, len(found.Buckets)),
	}

	for _, bucket := range found.Buckets {
		body.Buckets = append(body.Buckets, bucketResponse{
			At:            bucket.At.Format(time.RFC3339Nano),
			Samples:       bucket.Samples,
			CPUAverage:    round(bucket.CPUAverage),
			CPUPeak:       round(bucket.CPUPeak),
			MemoryAverage: bucket.MemoryAverage,
			MemoryPeak:    bucket.MemoryPeak,
			MemoryMiB:     bucket.MemoryMiB,
		})
	}

	httpx.Write(w, http.StatusOK, body)
}
