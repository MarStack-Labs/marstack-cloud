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
	ID     string `json:"id"`
	Name   string `json:"name"`
	Zone   string `json:"zone,omitempty"`
	Status string `json:"status"`
}

type instanceView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Image           string   `json:"image"`
	Command         []string `json:"command,omitempty"`
	VCPU            int      `json:"vcpu"`
	MemoryMiB       int      `json:"memory_mib"`
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

type networkView struct {
	NetworkID string    `json:"network_id"`
	Name      string    `json:"name"`
	Bridge    string    `json:"bridge"`
	CIDR      string    `json:"cidr"`
	Gateway   string    `json:"gateway"`
	Slice     string    `json:"slice"`
	NICs      []nicView `json:"nics"`
}

type networkListBody struct {
	Networks []networkView `json:"networks"`
}

type statusBody struct {
	ObservedState string `json:"observed_state"`
	Message       string `json:"message,omitempty"`
}

type registerBody struct {
	Name         string `json:"name"`
	Zone         string `json:"zone,omitempty"`
	Arch         string `json:"arch"`
	OS           string `json:"os"`
	CPUs         int    `json:"cpus"`
	MemoryMiB    int    `json:"memory_mib"`
	AgentVersion string `json:"agent_version"`
}

type client struct {
	endpoint string
	http     *http.Client
}

func newClient(endpoint string) *client {
	return &client{
		endpoint: strings.TrimRight(endpoint, "/"),
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

func (c *client) nodeNetworks(ctx context.Context, nodeID string) ([]networkView, error) {
	var out networkListBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/network", nil, &out)
	return out.Networks, err
}

func (c *client) reportStatus(ctx context.Context, nodeID, instanceID, observed, message string) error {
	return c.do(ctx, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/status",
		statusBody{ObservedState: observed, Message: message}, nil)
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
