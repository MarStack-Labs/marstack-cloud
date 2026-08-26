package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	callTimeout  = 15 * time.Second
	maxErrorBody = 1 << 16
)

type nodeView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Zone    string `json:"zone,omitempty"`
	Address string `json:"address,omitempty"`
	Status  string `json:"status"`
}

type nodeListBody struct {
	Nodes []nodeView `json:"nodes"`
}

type instanceView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Isolation       string   `json:"isolation"`
	Image           string   `json:"image"`
	ISO             string   `json:"iso,omitempty"`
	Kernel          string   `json:"kernel,omitempty"`
	DiskGiB         int      `json:"disk_gib,omitempty"`
	Command         []string `json:"command,omitempty"`
	VCPU            int      `json:"vcpu"`
	MemoryMiB       int      `json:"memory_mib"`
	RestartPolicy   string   `json:"restart_policy,omitempty"`
	RestartCount    int      `json:"restart_count,omitempty"`
	DesiredState    string   `json:"desired_state"`
	ObservedState   string   `json:"observed_state"`
	ObservedMessage string   `json:"observed_message,omitempty"`
}

type instanceListBody struct {
	Instances []instanceView `json:"instances"`
}

type nicView struct {
	InstanceID string `json:"instance_id"`
	IP         string `json:"ip"`
	MAC        string `json:"mac"`
}

type peerView struct {
	NodeID string `json:"node_id"`
	Slice  string `json:"slice"`
}

type networkView struct {
	NetworkID string     `json:"network_id"`
	Name      string     `json:"name"`
	Bridge    string     `json:"bridge"`
	CIDR      string     `json:"cidr"`
	Gateway   string     `json:"gateway"`
	Slice     string     `json:"slice"`
	NICs      []nicView  `json:"nics"`
	Peers     []peerView `json:"peers"`
}

type networkListBody struct {
	Networks []networkView `json:"networks"`
}

type statusBody struct {
	ObservedState string `json:"observed_state"`
	Message       string `json:"message,omitempty"`
	Restarts      int    `json:"restarts,omitempty"`
}

type registerBody struct {
	Name         string `json:"name"`
	Zone         string `json:"zone,omitempty"`
	Address      string `json:"address,omitempty"`
	Arch         string `json:"arch"`
	OS           string `json:"os"`
	CPUs         int    `json:"cpus"`
	MemoryMiB    int    `json:"memory_mib"`
	AgentVersion string `json:"agent_version"`
}

type client struct {
	endpoint string
	secret   string
	http     *http.Client
}

func newClient(endpoint, secret string) *client {
	return &client{
		endpoint: strings.TrimRight(endpoint, "/"),
		secret:   secret,
		http:     &http.Client{Timeout: callTimeout},
	}
}

func (c *client) register(ctx context.Context, body registerBody) (nodeView, error) {
	var out nodeView
	err := c.do(ctx, http.MethodPost, "/v1/nodes/register", body, &out)
	return out, err
}

func (c *client) heartbeat(ctx context.Context, nodeID string) (nodeView, error) {
	var out nodeView
	err := c.do(ctx, http.MethodPost, "/v1/nodes/"+nodeID+"/heartbeat", nil, &out)
	return out, err
}

func (c *client) assignedInstances(ctx context.Context, nodeID string) ([]instanceView, error) {
	var out instanceListBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/instances", nil, &out)
	return out.Instances, err
}

func (c *client) nodes(ctx context.Context) ([]nodeView, error) {
	var out nodeListBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes", nil, &out)
	return out.Nodes, err
}

func (c *client) nodeNetworks(ctx context.Context, nodeID string) ([]networkView, error) {
	var out networkListBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/network", nil, &out)
	return out.Networks, err
}

type dnsRecordView struct {
	FQDN string `json:"fqdn"`
	IP   string `json:"ip"`
}

type imageView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Arch     string `json:"arch"`
	Source   string `json:"source"`
	Checksum string `json:"checksum,omitempty"`
}

type volumeView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	SizeGiB     int            `json:"size_gib"`
	InstanceID  string         `json:"instance_id,omitempty"`
	RestoreFrom string         `json:"restore_from,omitempty"`
	Snapshots   []snapshotView `json:"snapshots,omitempty"`
}

type snapshotView struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type reportedSnapshotBody struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type reportedVolumeBody struct {
	VolumeID  string                 `json:"volume_id"`
	Snapshots []reportedSnapshotBody `json:"snapshots"`
	Restored  string                 `json:"restored,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

type reportVolumesBody struct {
	Volumes []reportedVolumeBody `json:"volumes"`
}

type volumesBody struct {
	Volumes []volumeView `json:"volumes"`
}

type forwardView struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	Protocol   string `json:"protocol"`
	NodePort   int    `json:"node_port"`
	TargetPort int    `json:"target_port"`
	Address    string `json:"address"`
}

type forwardsBody struct {
	Forwards []forwardView `json:"forwards"`
}

type imagesBody struct {
	Images []imageView `json:"images"`
}

type dnsRecordsBody struct {
	Records []dnsRecordView `json:"records"`
}

func (c *client) dnsRecords(ctx context.Context) ([]dnsRecordView, error) {
	var out dnsRecordsBody
	err := c.do(ctx, http.MethodGet, "/v1/dns/records", nil, &out)
	return out.Records, err
}

func (c *client) volumes(ctx context.Context, nodeID string) ([]volumeView, error) {
	var out volumesBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/volumes", nil, &out)
	return out.Volumes, err
}

func (c *client) reportVolumes(ctx context.Context, nodeID string, reports []reportedVolumeBody) error {
	return c.do(ctx, http.MethodPut, "/v1/nodes/"+nodeID+"/volumes",
		reportVolumesBody{Volumes: reports}, nil)
}

func (c *client) forwards(ctx context.Context, nodeID string) ([]forwardView, error) {
	var out forwardsBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/forwards", nil, &out)
	return out.Forwards, err
}

func (c *client) images(ctx context.Context) ([]imageView, error) {
	var out imagesBody
	err := c.do(ctx, http.MethodGet, "/v1/images", nil, &out)
	return out.Images, err
}

type stagedImageBody struct {
	ImageID   string `json:"image_id"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type reportImagesBody struct {
	Images []stagedImageBody `json:"images"`
}

func (c *client) reportImages(ctx context.Context, nodeID string, staged []stagedImageBody) error {
	return c.do(ctx, http.MethodPut, "/v1/nodes/"+nodeID+"/images",
		reportImagesBody{Images: staged}, nil)
}

func (c *client) reportStatus(
	ctx context.Context, nodeID, instanceID, observed, message string, restarts int,
) error {
	return c.do(ctx, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/status",
		statusBody{ObservedState: observed, Message: message, Restarts: restarts}, nil)
}

type statusError struct {
	Status int
	Code   string
}

func (e *statusError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("control plane returned %d", e.Status)
	}
	return fmt.Sprintf("control plane returned %d: %s", e.Status, e.Code)
}

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call control plane: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode >= http.StatusBadRequest {
		return &statusError{Status: res.StatusCode, Code: errorCode(res.Body)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func errorCode(body io.Reader) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBody))
	if err != nil {
		return ""
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return envelope.Error.Code
}
