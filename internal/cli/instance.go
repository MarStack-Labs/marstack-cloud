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

var instanceHeaders = []string{"NAME", "ID", "ISOLATION", "IMAGE", "VCPU", "MEMORY", "DESIRED", "OBSERVED", "MESSAGE"}

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
		in.Isolation,
		in.Image,
		strconv.Itoa(in.VCPU),
		strconv.Itoa(in.MemoryMiB) + "Mi",
		in.Desired,
		in.Observed,
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
	)
	return cmd
}

func newInstanceCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name      string   `json:"name"`
		Isolation string   `json:"isolation"`
		Image     string   `json:"image"`
		Command   []string `json:"command,omitempty"`
		VCPU      int      `json:"vcpu,omitempty"`
		MemoryMiB int      `json:"memory_mib,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create [-- command args...]",
		Short: "Create an instance",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Command = args

			var created instanceView
			if err := newClient(g.endpoint).do(
				cmd.Context(), "POST", "/v1/instances", req, &created,
			); err != nil {
				return err
			}
			return renderInstance(cmd, g, created)
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "instance name, unique within the platform")
	cmd.Flags().StringVar(&req.Isolation, "isolation", "container", "isolation: container, vm, microvm")
	cmd.Flags().StringVar(&req.Image, "image", "", "image the instance boots from")
	cmd.Flags().IntVar(&req.VCPU, "vcpu", 0, "virtual CPUs, defaults to the platform default")
	cmd.Flags().IntVar(&req.MemoryMiB, "memory-mib", 0, "memory in MiB, defaults to the platform default")

	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("image"))

	return cmd
}

func newInstanceListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list instanceListView
			if err := newClient(g.endpoint).do(
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
			if err := newClient(g.endpoint).do(
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
			if err := newClient(g.endpoint).do(
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
			if err := newClient(g.endpoint).do(
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
