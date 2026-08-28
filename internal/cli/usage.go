package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type nodeUsageView struct {
	NodeID        string  `json:"node_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMiB int     `json:"memory_used_mib"`
	MemoryMiB     int     `json:"memory_mib"`
	ReportedAt    string  `json:"reported_at"`
}

type instanceUsageView struct {
	InstanceID    string  `json:"instance_id"`
	NodeID        string  `json:"node_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMiB int     `json:"memory_used_mib"`
	ReportedAt    string  `json:"reported_at"`
}

type usageView struct {
	Nodes     []nodeUsageView     `json:"nodes"`
	Instances []instanceUsageView `json:"instances"`
}

var usageHeaders = []string{"SCOPE", "ID", "CPU", "MEMORY", "REPORTED"}

func percent(value float64) string {
	return strconv.FormatFloat(value, 'f', 1, 64) + "%"
}

func newUsageCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Show what each node and instance is actually using",
		Long: "Show what each node and instance is actually using.\n\n" +
			"Instance usage is scoped to your project. Node load is administrative, since it\n" +
			"is infrastructure rather than yours, so a member sees only the instance rows.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var view usageView
			if err := g.client().do(cmd.Context(), "GET", "/v1/usage", nil, &view); err != nil {
				return err
			}

			var nodes usageView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/usage/nodes", nil, &nodes,
			); err == nil {
				view.Nodes = nodes.Nodes
			}

			rows := make([][]string, 0, len(view.Nodes)+len(view.Instances))
			for _, node := range view.Nodes {
				rows = append(rows, []string{
					"node", node.NodeID, percent(node.CPUPercent),
					strconv.Itoa(node.MemoryUsedMiB) + "/" + strconv.Itoa(node.MemoryMiB) + "Mi",
					shortStamp(node.ReportedAt),
				})
			}
			for _, in := range view.Instances {
				rows = append(rows, []string{
					"instance", in.InstanceID, percent(in.CPUPercent),
					strconv.Itoa(in.MemoryUsedMiB) + "Mi",
					shortStamp(in.ReportedAt),
				})
			}

			return render(cmd.OutOrStdout(), g.output, view, table{headers: usageHeaders, rows: rows})
		},
	}
}

func shortStamp(value string) string {
	if len(value) > 19 {
		return value[:19]
	}
	return value
}
