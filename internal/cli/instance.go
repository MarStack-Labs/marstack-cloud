package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type instanceView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Isolation       string `json:"isolation"`
	Image           string `json:"image"`
	VCPU            int    `json:"vcpu"`
	MemoryMiB       int    `json:"memory_mib"`
	RestartPolicy   string `json:"restart_policy,omitempty"`
	RestartCount    int    `json:"restart_count,omitempty"`
	Desired         string `json:"desired_state"`
	Observed        string `json:"observed_state"`
	ObservedMessage string `json:"observed_message,omitempty"`
	NodeID          string `json:"node_id,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type instanceListView struct {
	Instances []instanceView `json:"instances"`
}

var instanceHeaders = []string{"NAME", "ID", "IMAGE", "SIZE", "DESIRED", "OBSERVED", "RESTARTS", "MESSAGE"}

func instanceRow(in instanceView) []string {
	message := in.ObservedMessage
	if message == "" {
		message = "-"
	}
	if len(message) > 60 {
		message = message[:57] + "..."
	}

	return []string{
		in.Name,
		in.ID,
		in.Image,
		strconv.Itoa(in.VCPU) + "cpu/" + strconv.Itoa(in.MemoryMiB) + "Mi",
		in.Desired,
		in.Observed,
		strconv.Itoa(in.RestartCount),
		message,
	}
}

func newInstanceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "instance",
		Short:   "Manage instances",
		Aliases: []string{"instances"},
	}
	cmd.AddCommand(
		newInstanceCreateCmd(g),
		newInstanceListCmd(g),
		newInstanceGetCmd(g),
		newInstanceStartCmd(g),
		newInstanceStopCmd(g),
		newInstanceDeleteCmd(g),
		newInstanceConsoleCmd(),
	)
	return cmd
}

func newInstanceCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name          string   `json:"name"`
		Isolation     string   `json:"isolation"`
		Image         string   `json:"image"`
		ISO           string   `json:"iso,omitempty"`
		Kernel        string   `json:"kernel,omitempty"`
		DiskGiB       int      `json:"disk_gib,omitempty"`
		FirewallID    string   `json:"firewall_id,omitempty"`
		Command       []string `json:"command,omitempty"`
		RestartPolicy string   `json:"restart_policy,omitempty"`
		VCPU          int      `json:"vcpu,omitempty"`
		MemoryMiB     int      `json:"memory_mib,omitempty"`
		Group         string   `json:"placement_group,omitempty"`
		Strict        bool     `json:"placement_strict,omitempty"`
		Keys          []string `json:"keys,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create [-- command args...]",
		Short: "Create an instance",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Command = args

			var created instanceView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/instances", req, &created,
			); err != nil {
				return err
			}
			return renderInstance(cmd, g, created)
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "instance name, unique within the platform")
	cmd.Flags().StringVar(&req.Isolation, "isolation", "container",
		"isolation: container, vm, microvm or sandbox")
	cmd.Flags().StringVar(&req.Image, "image", "",
		"image the instance boots from: an OCI reference for container, microvm and sandbox, "+
			"or a registered disk image for vm")
	cmd.Flags().StringVar(&req.ISO, "iso", "",
		"registered iso image to attach to a vm and boot first, for installing an os yourself")
	cmd.Flags().StringVar(&req.Kernel, "kernel", "",
		"registered kernel image a microvm or sandbox boots, instead of the one staged on the node")
	cmd.Flags().IntVar(&req.DiskGiB, "disk-gib", 0,
		"size of a vm disk created without a base image, in GiB")
	cmd.Flags().StringVar(&req.FirewallID, "firewall", "",
		"firewall id whose rules decide what may reach this instance")
	cmd.Flags().StringVar(&req.RestartPolicy, "restart", "",
		"restart policy: always, on-failure, never")
	cmd.Flags().IntVar(&req.VCPU, "vcpu", 0, "virtual CPUs, defaults to the platform default")
	cmd.Flags().IntVar(&req.MemoryMiB, "memory-mib", 0, "memory in MiB, defaults to the platform default")
	cmd.Flags().StringVar(&req.Group, "placement-group", "",
		"instances sharing this name are spread across nodes and zones")
	cmd.Flags().BoolVar(&req.Strict, "placement-strict", false,
		"hold this instance pending rather than share a node with its group")
	cmd.Flags().StringArrayVar(&req.Keys, "key", nil,
		"ssh key to install at first boot, by name; repeat for more, isolation vm only")

	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newInstanceListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list instanceListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/instances", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Instances))
			for _, in := range list.Instances {
				rows = append(rows, instanceRow(in))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: instanceHeaders, rows: rows})
		},
	}
}

func newInstanceGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var in instanceView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/instances/"+args[0], nil, &in,
			); err != nil {
				return err
			}
			return renderInstance(cmd, g, in)
		},
	}
}

func newInstanceStartCmd(g *globals) *cobra.Command {
	return newInstanceTransitionCmd(g, "start", "Ask the platform to run an instance")
}

func newInstanceStopCmd(g *globals) *cobra.Command {
	return newInstanceTransitionCmd(g, "stop", "Ask the platform to stop an instance")
}

func newInstanceTransitionCmd(g *globals, verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var in instanceView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/instances/"+args[0]+"/"+verb, nil, &in,
			); err != nil {
				return err
			}
			return renderInstance(cmd, g, in)
		},
	}
}

func newInstanceDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/instances/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}

func renderInstance(cmd *cobra.Command, g *globals, in instanceView) error {
	return render(cmd.OutOrStdout(), g.output, in, table{
		headers: instanceHeaders,
		rows:    [][]string{instanceRow(in)},
	})
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
