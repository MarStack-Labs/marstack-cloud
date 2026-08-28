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
	Schedulable  bool   `json:"schedulable"`
	Draining     bool   `json:"draining"`
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

var nodeHeaders = []string{"NAME", "STATUS", "SCHEDULING", "ZONE", "ARCH", "CPUS", "MEMORY", "AGENT"}

func nodeRow(n nodeView) []string {
	zone := n.Zone
	if zone == "" {
		zone = "-"
	}
	scheduling := "open"
	switch {
	case n.Draining:
		scheduling = "draining"
	case !n.Schedulable:
		scheduling = "cordoned"
	}

	return []string{
		n.Name,
		n.Status,
		scheduling,
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
	cmd.AddCommand(newNodeListCmd(g), newNodeGetCmd(g),
		newNodeCordonCmd(g), newNodeUncordonCmd(g), newNodeDrainCmd(g))
	return cmd
}

func newNodeListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list nodeListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/nodes", nil, &list); err != nil {
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
			if err := g.client().do(cmd.Context(), "GET", "/v1/nodes/"+args[0], nil, &n); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, n, table{
				headers: nodeHeaders,
				rows:    [][]string{nodeRow(n)},
			})
		},
	}
}

func nodeAction(g *globals, use, short, long, verb, done string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var n nodeView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/nodes/"+args[0]+"/"+verb, nil, &n,
			); err != nil {
				return err
			}
			cmd.Printf("%s %s\n", n.Name, done)
			return render(cmd.OutOrStdout(), g.output, n, table{
				headers: nodeHeaders,
				rows:    [][]string{nodeRow(n)},
			})
		},
	}
}

func newNodeCordonCmd(g *globals) *cobra.Command {
	return nodeAction(g, "cordon <id>", "Stop placing new workloads on a node",
		"Stop placing new workloads on a node.\n\n"+
			"What is already running stays running and keeps serving. Use drain to move it.",
		"cordon", "will take no new workloads")
}

func newNodeUncordonCmd(g *globals) *cobra.Command {
	return nodeAction(g, "uncordon <id>", "Let a node take workloads again",
		"Let a node take workloads again.\n\n"+
			"This also cancels a drain in progress; anything already moved stays where it went.",
		"uncordon", "will take workloads again")
}

func newNodeDrainCmd(g *globals) *cobra.Command {
	return nodeAction(g, "drain <id>", "Cordon a node and move what can move off it",
		"Cordon a node and move what can move off it.\n\n"+
			"This returns as soon as the intent is recorded; the scheduler does the moving on\n"+
			"its next pass, so watch the node until it stops draining.\n\n"+
			"Only containers move. Anything with a disk on that node stays, because moving it\n"+
			"would lose the disk, and the drain reports it rather than pretending otherwise.",
		"drain", "is draining")
}
