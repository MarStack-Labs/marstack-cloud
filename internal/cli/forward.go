package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type forwardView struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	Protocol   string `json:"protocol"`
	NodePort   int    `json:"node_port"`
	TargetPort int    `json:"target_port"`
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	CreatedAt  string `json:"created_at"`
}

type forwardListView struct {
	Forwards []forwardView `json:"forwards"`
}

var forwardHeaders = []string{"ID", "NODE PORT", "TARGET", "INSTANCE", "NODE"}

func forwardRow(f forwardView) []string {
	return []string{
		f.ID,
		strconv.Itoa(f.NodePort) + "/" + f.Protocol,
		f.Address + ":" + strconv.Itoa(f.TargetPort),
		f.InstanceID,
		f.NodeID,
	}
}

func newForwardCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "forward",
		Short:   "Publish an instance port on the node that runs it",
		Aliases: []string{"forwards"},
	}
	cmd.AddCommand(newForwardCreateCmd(g), newForwardListCmd(g), newForwardDeleteCmd(g))
	return cmd
}

func newForwardCreateCmd(g *globals) *cobra.Command {
	var req struct {
		InstanceID string `json:"instance_id"`
		Protocol   string `json:"protocol,omitempty"`
		NodePort   int    `json:"node_port,omitempty"`
		TargetPort int    `json:"target_port"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Publish a port, reachable on the address the node reported",
		Long: "Publish a port, reachable on the address the node reported.\n\n" +
			"The node rewrites arriving packets to the instance with nftables. Nothing binds a\n" +
			"socket, so a privileged node port is fine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created forwardView
			if err := g.client().do(cmd.Context(), "POST", "/v1/forwards", req, &created); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: forwardHeaders,
				rows:    [][]string{forwardRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.InstanceID, "instance", "", "instance id whose port is published")
	cmd.Flags().IntVar(&req.TargetPort, "target-port", 0, "port inside the instance")
	cmd.Flags().IntVar(&req.NodePort, "node-port", 0, "port on the node, defaults to the target port")
	cmd.Flags().StringVar(&req.Protocol, "protocol", "tcp", "tcp or udp")
	must(cmd.MarkFlagRequired("instance"))
	must(cmd.MarkFlagRequired("target-port"))

	return cmd
}

func newForwardListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List published ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list forwardListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/forwards", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Forwards))
			for _, f := range list.Forwards {
				rows = append(rows, forwardRow(f))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: forwardHeaders, rows: rows})
		},
	}
}

func newForwardDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Stop publishing a port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/forwards/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
