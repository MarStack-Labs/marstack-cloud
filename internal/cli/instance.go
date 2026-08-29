package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type instanceView struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	NodeSelector    map[string]string `json:"node_selector,omitempty"`
	EnvNames        []string          `json:"env_names,omitempty"`
	FilePaths       []string          `json:"file_paths,omitempty"`
	ExtraNetworks   []string          `json:"extra_networks,omitempty"`
	Isolation       string            `json:"isolation"`
	Image           string            `json:"image"`
	VCPU            int               `json:"vcpu"`
	MemoryMiB       int               `json:"memory_mib"`
	RestartPolicy   string            `json:"restart_policy,omitempty"`
	RestartCount    int               `json:"restart_count,omitempty"`
	Desired         string            `json:"desired_state"`
	Observed        string            `json:"observed_state"`
	ObservedMessage string            `json:"observed_message,omitempty"`
	NodeID          string            `json:"node_id,omitempty"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
}

type instanceListView struct {
	Instances []instanceView `json:"instances"`
	Next      string         `json:"next,omitempty"`
}

func (v instanceListView) cursor() string { return v.Next }

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
		newInstanceResizeCmd(g),
		newInstanceDeleteCmd(g),
		newInstanceConsoleCmd(),
	)
	return cmd
}

func newInstanceCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name          string            `json:"name"`
		Isolation     string            `json:"isolation"`
		Image         string            `json:"image"`
		ISO           string            `json:"iso,omitempty"`
		Kernel        string            `json:"kernel,omitempty"`
		DiskGiB       int               `json:"disk_gib,omitempty"`
		FirewallID    string            `json:"firewall_id,omitempty"`
		Command       []string          `json:"command,omitempty"`
		RestartPolicy string            `json:"restart_policy,omitempty"`
		VCPU          int               `json:"vcpu,omitempty"`
		MemoryMiB     int               `json:"memory_mib,omitempty"`
		Group         string            `json:"placement_group,omitempty"`
		Strict        bool              `json:"placement_strict,omitempty"`
		NodeSelector  map[string]string `json:"node_selector,omitempty"`
		Env           map[string]string `json:"env,omitempty"`
		Files         []fileSpec        `json:"files,omitempty"`
		NetworkID     string            `json:"network_id,omitempty"`
		ExtraNetworks []string          `json:"extra_networks,omitempty"`
		Keys          []string          `json:"keys,omitempty"`
	}
	var (
		selectors []string
		envPairs  []string
		filePairs []string
		networks  []string
	)

	cmd := &cobra.Command{
		Use:   "create [-- command args...]",
		Short: "Create an instance",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Command = args

			for _, pair := range selectors {
				key, value, found := strings.Cut(pair, "=")
				if !found {
					return errors.New("a node selector is key=value, and " + pair + " has no value")
				}
				if req.NodeSelector == nil {
					req.NodeSelector = map[string]string{}
				}
				req.NodeSelector[key] = value
			}

			for _, pair := range envPairs {
				name, value, found := strings.Cut(pair, "=")
				if !found {
					return errors.New("an environment variable is NAME=value, and " +
						pair + " has no value")
				}
				if req.Env == nil {
					req.Env = map[string]string{}
				}
				req.Env[name] = value
			}

			if len(networks) > 0 {
				req.NetworkID = networks[0]
				req.ExtraNetworks = networks[1:]
			}

			files, err := readFiles(filePairs)
			if err != nil {
				return err
			}
			req.Files = files

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
	cmd.Flags().StringArrayVar(&envPairs, "env", nil,
		"NAME=value passed to the workload, repeatable; needs a control plane started with "+
			"--backup-key-file, and values are never served back")
	cmd.Flags().StringArrayVar(&networks, "network", nil,
		"network to attach, repeatable; the first is eth0 and carries the default route, "+
			"the rest are extra interfaces")
	cmd.Flags().StringArrayVar(&filePairs, "file", nil,
		"/path/in/the/workload=local-file[:mode] to write before it starts, repeatable; "+
			"content is sealed at rest and never served back")
	cmd.Flags().StringArrayVar(&selectors, "node-selector", nil,
		"only place on a node carrying key=value, repeatable and combined with and")
	cmd.Flags().StringArrayVar(&req.Keys, "key", nil,
		"ssh key to install at first boot, by name; repeat for more, isolation vm only")

	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newInstanceResizeCmd(g *globals) *cobra.Command {
	var body struct {
		VCPU      int `json:"vcpu,omitempty"`
		MemoryMiB int `json:"memory_mib,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "resize <id>",
		Short: "Change how much cpu and memory an instance may use",
		Long: "Change how much cpu and memory an instance may use.\n\n" +
			"A container takes the new limits on the next reconcile pass without restarting,\n" +
			"because cgroup limits are writable while it runs. A vm, microvm or sandbox keeps\n" +
			"the size it booted with until it is stopped and started again - nothing here\n" +
			"hot-plugs cpu or memory into a guest.\n\n" +
			"Leave a flag off to keep that dimension as it is.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if body.VCPU == 0 && body.MemoryMiB == 0 {
				return errors.New("give --vcpu, --memory-mib, or both")
			}

			var resized instanceView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/instances/"+args[0]+"/resize", body, &resized,
			); err != nil {
				return err
			}
			if resized.Isolation != "container" {
				cmd.PrintErrln("this isolation takes the new size on its next start, not now")
			}
			return renderInstance(cmd, g, resized)
		},
	}

	cmd.Flags().IntVar(&body.VCPU, "vcpu", 0, "virtual CPUs")
	cmd.Flags().IntVar(&body.MemoryMiB, "memory-mib", 0, "memory in MiB")
	return cmd
}

func newInstanceListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var all instanceListView

			if err := walkPages(cmd.Context(), g.client(), "/v1/instances",
				func(p instanceListView) {
					all.Instances = append(all.Instances, p.Instances...)
				},
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(all.Instances))
			for _, in := range all.Instances {
				rows = append(rows, instanceRow(in))
			}
			return render(cmd.OutOrStdout(), g.output, all, table{headers: instanceHeaders, rows: rows})
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
