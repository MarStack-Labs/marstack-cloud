package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type nodeView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Zone         string `json:"zone,omitempty"`
	Status       string `json:"status"`
	Arch         string `json:"arch"`
	OS           string `json:"os"`
	CPUs         int    `json:"cpus"`
	MemoryMiB    int    `json:"memory_mib"`
	AgentVersion string `json:"agent_version"`
	RegisteredAt string `json:"registered_at"`
	LastSeenAt   string `json:"last_seen_at"`
}

type nodeListView struct {
	Nodes []nodeView `json:"nodes"`
}

var nodeHeaders = []string{"NAME", "ID", "STATUS", "ZONE", "ARCH", "CPUS", "MEMORY", "AGENT"}

func nodeRow(n nodeView) []string {
	zone := n.Zone
	if zone == "" {
		zone = "-"
	}
	return []string{
		n.Name,
		n.ID,
		n.Status,
		zone,
		n.Arch,
		strconv.Itoa(n.CPUs),
		strconv.Itoa(n.MemoryMiB) + "Mi",
		n.AgentVersion,
	}
}

func newNodeCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "node",
		Short:   "Inspect nodes",
		Aliases: []string{"nodes"},
	}
	cmd.AddCommand(newNodeListCmd(g), newNodeGetCmd(g))
	return cmd
}

func newNodeListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list nodeListView
			if err := newClient(g.endpoint).do(cmd.Context(), "GET", "/v1/nodes", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Nodes))
			for _, n := range list.Nodes {
				rows = append(rows, nodeRow(n))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: nodeHeaders, rows: rows})
		},
	}
}

func newNodeGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var n nodeView
			if err := newClient(g.endpoint).do(cmd.Context(), "GET", "/v1/nodes/"+args[0], nil, &n); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, n, table{
				headers: nodeHeaders,
				rows:    [][]string{nodeRow(n)},
			})
		},
	}
}
