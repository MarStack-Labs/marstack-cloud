package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	callTimeout     = 15 * time.Second
	transferTimeout = 30 * time.Minute
	maxErrorBody    = 1 << 16
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
	DeviceAddress   string            `json:"device_address,omitempty"`
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Isolation       string            `json:"isolation"`
	Image           string            `json:"image"`
	ISO             string            `json:"iso,omitempty"`
	Kernel          string            `json:"kernel,omitempty"`
	FirewallID      string            `json:"firewall_id,omitempty"`
	DiskGiB         int               `json:"disk_gib,omitempty"`
	Command         []string          `json:"command,omitempty"`
	VCPU            int               `json:"vcpu"`
	MemoryMiB       int               `json:"memory_mib"`
	RestartPolicy   string            `json:"restart_policy,omitempty"`
	RestartCount    int               `json:"restart_count,omitempty"`
	SSHKeys         []string          `json:"ssh_keys,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	Files           []fileView        `json:"files,omitempty"`
	DesiredState    string            `json:"desired_state"`
	ObservedState   string            `json:"observed_state"`
	ObservedMessage string            `json:"observed_message,omitempty"`
	ExitCode        *int              `json:"exit_code,omitempty"`
	Migrating       bool              `json:"migrating,omitempty"`
	DiskParked      bool              `json:"disk_parked,omitempty"`
}

type fileView struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"`
}

type instanceListBody struct {
	Instances []instanceView `json:"instances"`
}

type nicView struct {
	InstanceID string `json:"instance_id"`
	Device     int    `json:"device"`
	IP         string `json:"ip"`
	IP6        string `json:"ip6,omitempty"`
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
	CIDR6     string     `json:"cidr6,omitempty"`
	Gateway6  string     `json:"gateway6,omitempty"`
	Slice     string     `json:"slice"`
	Slice6    string     `json:"slice6,omitempty"`
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
	ExitCode      *int   `json:"exit_code,omitempty"`
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
	transfer *http.Client
	stream   *http.Client
}

func newClient(endpoint, secret string, trusted *tls.Config) *client {
	transport := http.DefaultTransport
	if trusted != nil {
		cloned := http.DefaultTransport.(*http.Transport).Clone()
		cloned.TLSClientConfig = trusted
		transport = cloned
	}

	return &client{
		endpoint: strings.TrimRight(endpoint, "/"),
		secret:   secret,
		http:     &http.Client{Timeout: callTimeout, Transport: transport},
		transfer: &http.Client{Timeout: transferTimeout, Transport: transport},
		stream:   &http.Client{Transport: transport},
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
	BackupID    string         `json:"backup_id,omitempty"`
	CloneFrom   string         `json:"clone_from,omitempty"`
	CloneSnap   string         `json:"clone_snap,omitempty"`
	Detaching   bool           `json:"detaching,omitempty"`
	Encrypted   bool           `json:"encrypted,omitempty"`
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
	Detached  bool                   `json:"detached,omitempty"`
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
	ID          string `json:"id"`
	InstanceID  string `json:"instance_id"`
	Protocol    string `json:"protocol"`
	NodePort    int    `json:"node_port"`
	TargetPort  int    `json:"target_port"`
	Address     string `json:"address"`
	Certificate string `json:"certificate,omitempty"`
	PrivateKey  string `json:"private_key,omitempty"`
}

func (f forwardView) terminatesTLS() bool {
	return f.Certificate != "" && f.PrivateKey != ""
}

type firewallRuleView struct {
	Protocol string `json:"protocol,omitempty"`
	FromPort int    `json:"from_port,omitempty"`
	ToPort   int    `json:"to_port,omitempty"`
	Source   string `json:"source,omitempty"`
}

type firewallView struct {
	ID    string             `json:"id"`
	Name  string             `json:"name"`
	Rules []firewallRuleView `json:"rules"`
}

type instanceUsageBody struct {
	InstanceID    string  `json:"instance_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMiB int     `json:"memory_used_mib"`
}

type usageBody struct {
	CPUPercent    float64             `json:"cpu_percent"`
	MemoryUsedMiB int                 `json:"memory_used_mib"`
	MemoryMiB     int                 `json:"memory_mib"`
	Instances     []instanceUsageBody `json:"instances,omitempty"`
}

type firewallsBody struct {
	Firewalls []firewallView `json:"firewalls"`
}

type forwardsBody struct {
	Forwards []forwardView `json:"forwards"`
}

type balancerBackendView struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address,omitempty"`
	Healthy    bool   `json:"healthy"`
}

type balancerView struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Protocol    string                `json:"protocol"`
	ListenPort  int                   `json:"listen_port"`
	TargetPort  int                   `json:"target_port"`
	Algorithm   string                `json:"algorithm"`
	Check       string                `json:"check"`
	CheckPath   string                `json:"check_path,omitempty"`
	Rise        int                   `json:"rise,omitempty"`
	Fall        int                   `json:"fall,omitempty"`
	Backends    []balancerBackendView `json:"backends"`
	Routes      []balancerRouteView   `json:"routes,omitempty"`
	Certificate string                `json:"certificate,omitempty"`
	PrivateKey  string                `json:"private_key,omitempty"`
}

type balancerRouteView struct {
	Host     string                `json:"host,omitempty"`
	Path     string                `json:"path,omitempty"`
	Service  string                `json:"service"`
	Backends []balancerBackendView `json:"backends"`
}

func (b balancerView) terminatesTLS() bool {
	return b.Certificate != "" && b.PrivateKey != ""
}

func (b balancerView) runsInUserspace() bool {
	return b.terminatesTLS() || len(b.Routes) > 0
}

type healthReportBody struct {
	BalancerID string `json:"balancer_id"`
	InstanceID string `json:"instance_id"`
	Healthy    bool   `json:"healthy"`
	Reason     string `json:"reason,omitempty"`
}

type healthReportsBody struct {
	Checks []healthReportBody `json:"checks"`
}

type balancersBody struct {
	Balancers []balancerView `json:"balancers"`
}

type registryView struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type acmeChallengeView struct {
	Token         string `json:"token"`
	Authorization string `json:"authorization"`
}

type acmeChallengesBody struct {
	Challenges []acmeChallengeView `json:"challenges"`
}

type registriesBody struct {
	Credentials []registryView `json:"credentials"`
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

func (c *client) balancers(ctx context.Context, nodeID string) ([]balancerView, error) {
	var out balancersBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/balancers", nil, &out)
	return out.Balancers, err
}

func (c *client) reportHealth(ctx context.Context, nodeID string, reports []healthReportBody) error {
	return c.do(ctx, http.MethodPut, "/v1/nodes/"+nodeID+"/balancers/health",
		healthReportsBody{Checks: reports}, nil)
}

func (c *client) reportUsage(ctx context.Context, nodeID string, body usageBody) error {
	return c.do(ctx, http.MethodPut, "/v1/nodes/"+nodeID+"/usage", body, nil)
}

func (c *client) reportLogs(ctx context.Context, nodeID, instanceID string,
	lines []string) error {
	body := struct {
		Lines []string `json:"lines"`
	}{Lines: lines}
	return c.do(ctx, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/logs", body, nil)
}

func (c *client) firewalls(ctx context.Context, nodeID string) ([]firewallView, error) {
	var out firewallsBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/firewalls", nil, &out)
	return out.Firewalls, err
}

func (c *client) images(ctx context.Context, nodeID string) ([]imageView, error) {
	var out imagesBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/images", nil, &out)
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
	exitCode *int,
) error {
	return c.do(ctx, http.MethodPut,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/status",
		statusBody{ObservedState: observed, Message: message, Restarts: restarts,
			ExitCode: exitCode}, nil)
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
	if out == nil || res.StatusCode == http.StatusNoContent {
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

type backupView struct {
	ID       string `json:"id"`
	VolumeID string `json:"volume_id"`
	Name     string `json:"name"`
	State    string `json:"state"`
}

type backupsBody struct {
	Backups []backupView `json:"backups"`
}

type backupFailureBody struct {
	Message string `json:"message"`
}

func (c *client) pendingBackups(ctx context.Context, nodeID string) ([]backupView, error) {
	var out backupsBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/backups", nil, &out)
	return out.Backups, err
}

func (c *client) failBackup(ctx context.Context, nodeID, id, message string) error {
	return c.do(ctx, http.MethodPost,
		"/v1/nodes/"+nodeID+"/backups/"+id+"/failure", backupFailureBody{Message: message}, nil)
}

func (c *client) uploadBackup(ctx context.Context, nodeID, id string, content io.Reader) error {
	path := "/v1/nodes/" + nodeID + "/backups/" + id + "/content"

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.endpoint+path, content)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	res, err := c.transfer.Do(req)
	if err != nil {
		return fmt.Errorf("upload the backup: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode >= http.StatusBadRequest {
		return &statusError{Status: res.StatusCode, Code: errorCode(res.Body)}
	}
	return nil
}

func (c *client) fetchBackup(ctx context.Context, nodeID, id string) (io.ReadCloser, error) {
	path := "/v1/nodes/" + nodeID + "/backups/" + id + "/content"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	res, err := c.transfer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch the backup: %w", err)
	}
	if res.StatusCode >= http.StatusBadRequest {
		defer res.Body.Close()
		return nil, &statusError{Status: res.StatusCode, Code: errorCode(res.Body)}
	}
	return res.Body, nil
}

func (c *client) parkDisk(ctx context.Context, nodeID, instanceID string,
	content io.Reader) error {
	path := "/v1/nodes/" + nodeID + "/instances/" + instanceID + "/disk"

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.endpoint+path, content)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	res, err := c.transfer.Do(req)
	if err != nil {
		return fmt.Errorf("hand over the disk: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode >= http.StatusBadRequest {
		return &statusError{Status: res.StatusCode, Code: errorCode(res.Body)}
	}
	return nil
}

func (c *client) takeDisk(ctx context.Context, nodeID, instanceID string) (io.ReadCloser, error) {
	path := "/v1/nodes/" + nodeID + "/instances/" + instanceID + "/disk"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	res, err := c.transfer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch the disk: %w", err)
	}
	if res.StatusCode >= http.StatusBadRequest {
		defer res.Body.Close()
		return nil, &statusError{Status: res.StatusCode, Code: errorCode(res.Body)}
	}
	return res.Body, nil
}

func (c *client) diskLanded(ctx context.Context, nodeID, instanceID string) error {
	return c.do(ctx, http.MethodPost,
		"/v1/nodes/"+nodeID+"/instances/"+instanceID+"/landed", nil, nil)
}

const maxCommandOutput = 256 << 10

type commandView struct {
	ID             string   `json:"id"`
	InstanceID     string   `json:"instance_id"`
	Command        []string `json:"command"`
	Isolation      string   `json:"isolation"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

type commandResult struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (c *client) registries(ctx context.Context, nodeID string) ([]registryView, error) {
	var out registriesBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/registries", nil, &out)
	return out.Credentials, err
}

func (c *client) challenges(ctx context.Context, nodeID string) ([]acmeChallengeView, error) {
	var out acmeChallengesBody
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/acme-challenges", nil, &out)
	return out.Challenges, err
}

type shellView struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id"`
	Isolation  string   `json:"isolation"`
	Command    []string `json:"command"`
	State      string   `json:"state"`
}

func (c *client) takeShell(ctx context.Context, nodeID string) (shellView, bool, error) {
	var out shellView
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/shell", nil, &out)
	if err != nil {
		return shellView{}, false, err
	}
	return out, out.ID != "", nil
}

func (c *client) shellInput(
	ctx context.Context, nodeID, sessionID string,
) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint+"/v1/nodes/"+nodeID+"/shells/"+sessionID+"/input", nil)
	if err != nil {
		return nil, fmt.Errorf("build the input request: %w", err)
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	res, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("open the input stream: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("the control plane answered %d for the input stream",
			res.StatusCode)
	}
	return res.Body, nil
}

func (c *client) shellOutput(
	ctx context.Context, nodeID, sessionID string, body io.Reader,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+"/v1/nodes/"+nodeID+"/shells/"+sessionID+"/output", body)
	if err != nil {
		return fmt.Errorf("build the output request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	res, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("open the output stream: %w", err)
	}
	defer res.Body.Close()

	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

type deviceBody struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
	Vendor  string `json:"vendor,omitempty"`
	Product string `json:"product,omitempty"`
	Driver  string `json:"driver,omitempty"`
	Ready   bool   `json:"ready,omitempty"`
}

type devicesBody struct {
	Devices []deviceBody `json:"devices"`
}

func (c *client) reportDevices(
	ctx context.Context, nodeID string, held []deviceBody,
) error {
	return c.do(ctx, http.MethodPut, "/v1/nodes/"+nodeID+"/devices",
		devicesBody{Devices: held}, nil)
}

func (c *client) takeCommand(ctx context.Context, nodeID string) (commandView, bool, error) {
	var out commandView
	err := c.do(ctx, http.MethodGet, "/v1/nodes/"+nodeID+"/exec", nil, &out)
	if err != nil {
		return commandView{}, false, err
	}
	if out.ID == "" {
		return commandView{}, false, nil
	}
	return out, true, nil
}

func (c *client) finishCommand(ctx context.Context, nodeID, execID string,
	result commandResult) error {
	return c.do(ctx, http.MethodPost,
		"/v1/nodes/"+nodeID+"/execs/"+execID+"/result", result, nil)
}

type volumeKeyBody struct {
	VolumeID string `json:"volume_id"`
	Key      string `json:"key"`
}

func (c *client) volumeKey(ctx context.Context, nodeID, volumeID string) (string, error) {
	var out volumeKeyBody
	err := c.do(ctx, http.MethodGet,
		"/v1/nodes/"+nodeID+"/volumes/"+volumeID+"/key", nil, &out)
	return out.Key, err
}

type transferView struct {
	Direct    bool   `json:"direct"`
	URL       string `json:"url"`
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at"`
}

type uploadedBody struct {
	SizeBytes int64  `json:"size_bytes"`
	Checksum  string `json:"checksum"`
}

func (c *client) backupTransfer(
	ctx context.Context, nodeID, id, verb string,
) (transferView, error) {
	var out transferView
	err := c.do(ctx, http.MethodGet,
		"/v1/nodes/"+nodeID+"/backups/"+id+"/"+verb, nil, &out)
	return out, err
}

func (c *client) reportUploaded(
	ctx context.Context, nodeID, id string, body uploadedBody,
) error {
	return c.do(ctx, http.MethodPost,
		"/v1/nodes/"+nodeID+"/backups/"+id+"/uploaded", body, nil)
}
