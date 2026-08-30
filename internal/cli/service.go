package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type serviceMemberView struct {
	InstanceID string `json:"instance_id"`
	CreatedAt  string `json:"created_at"`
}

type serviceView struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Replicas  int                 `json:"replicas"`
	Isolation string              `json:"isolation"`
	Image     string              `json:"image"`
	Group     string              `json:"placement_group"`
	Blocked   string              `json:"blocked"`
	Members   []serviceMemberView `json:"members"`
}

type serviceListView struct {
	Services []serviceView `json:"services"`
}

var serviceHeaders = []string{"NAME", "ID", "REPLICAS", "ISOLATION", "IMAGE", "STATE"}

func serviceRow(s serviceView) []string {
	state := strconv.Itoa(len(s.Members)) + "/" + strconv.Itoa(s.Replicas) + " up"
	if s.Blocked != "" {
		state = "stuck: " + shortenLine(s.Blocked, 60)
	}

	return []string{s.Name, s.ID, strconv.Itoa(s.Replicas), s.Isolation, s.Image, state}
}

func shortenLine(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit-1] + "…"
}

func newServiceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "service",
		Short:   "Keep a number of replicas of one workload running",
		Aliases: []string{"services", "svc"},
	}
	cmd.AddCommand(
		newServiceCreateCmd(g),
		newServiceListCmd(g),
		newServiceGetCmd(g),
		newServiceScaleCmd(g),
		newServiceDeleteCmd(g),
	)
	return cmd
}

func newServiceCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name          string            `json:"name"`
		Replicas      int               `json:"replicas"`
		Isolation     string            `json:"isolation,omitempty"`
		Image         string            `json:"image,omitempty"`
		ISO           string            `json:"iso,omitempty"`
		Kernel        string            `json:"kernel,omitempty"`
		DiskGiB       int               `json:"disk_gib,omitempty"`
		FirewallID    string            `json:"firewall_id,omitempty"`
		Command       []string          `json:"command,omitempty"`
		NetworkID     string            `json:"network_id,omitempty"`
		RestartPolicy string            `json:"restart_policy,omitempty"`
		VCPU          int               `json:"vcpu,omitempty"`
		MemoryMiB     int               `json:"memory_mib,omitempty"`
		Group         string            `json:"placement_group,omitempty"`
		Strict        bool              `json:"placement_strict,omitempty"`
		NodeSelector  map[string]string `json:"node_selector,omitempty"`
		Keys          []string          `json:"keys,omitempty"`
	}
	var selectors []string

	cmd := &cobra.Command{
		Use:   "create [-- command args...]",
		Short: "Create a service and let the platform hold the replica count",
		Long: "Create a service and let the platform hold the replica count.\n\n" +
			"A service is a workload template plus a number. A loop makes replicas until the\n" +
			"count is met and replaces any that disappear, so a deleted or unrecoverable\n" +
			"replica comes back without anybody typing anything.\n\n" +
			"Replica names are the service name with a random suffix. Ordinals would collide\n" +
			"with an instance somebody made by hand and wedge the service permanently.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Command = args

			for _, pair := range selectors {
				key, value, found := strings.Cut(pair, "=")
				if !found {
					return errors.New(
						"a node selector is key=value, and " + pair + " has no value")
				}
				if req.NodeSelector == nil {
					req.NodeSelector = map[string]string{}
				}
				req.NodeSelector[key] = value
			}

			var created serviceView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/services", req, &created,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: serviceHeaders,
				rows:    [][]string{serviceRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "service name, unique in the project")
	cmd.Flags().IntVar(&req.Replicas, "replicas", 1, "how many replicas to hold")
	cmd.Flags().StringVar(&req.Isolation, "isolation", "container",
		"container, vm, microvm, or sandbox")
	cmd.Flags().StringVar(&req.Image, "image", "", "image every replica boots")
	cmd.Flags().StringVar(&req.ISO, "iso", "", "iso every replica boots")
	cmd.Flags().StringVar(&req.Kernel, "kernel", "", "kernel image for microvm and sandbox")
	cmd.Flags().IntVar(&req.DiskGiB, "disk-gib", 0, "root disk size for a vm")
	cmd.Flags().StringVar(&req.FirewallID, "firewall", "", "firewall every replica carries")
	cmd.Flags().StringVar(&req.NetworkID, "network", "", "network every replica joins")
	cmd.Flags().StringVar(&req.RestartPolicy, "restart", "", "never, on-failure, or always")
	cmd.Flags().IntVar(&req.VCPU, "vcpu", 0, "virtual CPUs per replica")
	cmd.Flags().IntVar(&req.MemoryMiB, "memory-mib", 0, "memory per replica in MiB")
	cmd.Flags().StringArrayVar(&selectors, "node-selector", nil,
		"only place replicas on nodes carrying key=value, repeatable and combined with and")
	cmd.Flags().StringVar(&req.Group, "placement-group", "",
		"spread replicas across zones and nodes")
	cmd.Flags().BoolVar(&req.Strict, "placement-strict", false,
		"refuse to place a replica that would break the spread")
	cmd.Flags().StringArrayVar(&req.Keys, "key", nil, "ssh key name, repeatable")
	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newServiceListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list serviceListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/services", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Services))
			for _, s := range list.Services {
				rows = append(rows, serviceRow(s))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: serviceHeaders, rows: rows})
		},
	}
}

var serviceMemberHeaders = []string{"REPLICA", "SINCE"}

func newServiceGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|id>",
		Short: "Show a service and the replicas it holds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var s serviceView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/services/"+args[0], nil, &s,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(s.Members))
			for _, member := range s.Members {
				rows = append(rows, []string{member.InstanceID,
					member.CreatedAt[:min(len(member.CreatedAt), 19)]})
			}
			return render(cmd.OutOrStdout(), g.output, s,
				table{headers: serviceMemberHeaders, rows: rows})
		},
	}
}

func newServiceScaleCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "scale <name|id> <replicas>",
		Short: "Change how many replicas a service holds",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			replicas, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}

			body := struct {
				Replicas int `json:"replicas"`
			}{Replicas: replicas}

			var scaled serviceView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/services/"+args[0]+"/scale", body, &scaled,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, scaled, table{
				headers: serviceHeaders,
				rows:    [][]string{serviceRow(scaled)},
			})
		},
	}
}

func newServiceDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a service and every replica it holds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/services/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s and its replicas\n", args[0])
			return nil
		},
	}
}
