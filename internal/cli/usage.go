package cli

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

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
	cmd := newUsageListCmd(g)
	cmd.AddCommand(newUsageHistoryCmd(g))
	return cmd
}

func newUsageListCmd(g *globals) *cobra.Command {
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

type bucketView struct {
	At            string  `json:"at"`
	Samples       int     `json:"samples"`
	CPUAverage    float64 `json:"cpu_average"`
	CPUPeak       float64 `json:"cpu_peak"`
	MemoryAverage int     `json:"memory_average"`
	MemoryPeak    int     `json:"memory_peak"`
	MemoryMiB     int     `json:"memory_mib"`
}

type historyView struct {
	Subject string       `json:"subject"`
	Window  string       `json:"window"`
	Buckets []bucketView `json:"buckets"`
}

var sparkLevels = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

func spark(values []float64, ceiling float64) string {
	if len(values) == 0 {
		return ""
	}
	if ceiling <= 0 {
		for _, v := range values {
			if v > ceiling {
				ceiling = v
			}
		}
	}
	if ceiling <= 0 {
		ceiling = 1
	}

	out := make([]rune, 0, len(values))
	for _, v := range values {
		level := int(v / ceiling * float64(len(sparkLevels)-1))
		if level < 0 {
			level = 0
		}
		if level >= len(sparkLevels) {
			level = len(sparkLevels) - 1
		}
		out = append(out, sparkLevels[level])
	}
	return string(out)
}

func summarise(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}

	var sum, peak float64
	for _, v := range values {
		sum += v
		if v > peak {
			peak = v
		}
	}
	return sum / float64(len(values)), peak
}

func newUsageHistoryCmd(g *globals) *cobra.Command {
	var window string

	cmd := &cobra.Command{
		Use:   "history <node-id|instance-id>",
		Short: "Show how a node or instance has been loaded",
		Long: "Show how a node or instance has been loaded.\n\n" +
			"Samples are folded into one minute buckets as they arrive and a day is kept, so\n" +
			"this answers whether something was busy earlier. It is not an archive: point a\n" +
			"real metrics stack at it if you need to keep more than that.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/usage/history?subject=" + url.QueryEscape(args[0])
			if strings.HasPrefix(args[0], "n-") {
				path = "/v1/usage/nodes/history?subject=" + url.QueryEscape(args[0])
			}
			if window != "" {
				path += "&window=" + url.QueryEscape(window)
			}

			var view historyView
			if err := g.client().do(cmd.Context(), "GET", path, nil, &view); err != nil {
				return err
			}
			if g.output == "json" {
				return render(cmd.OutOrStdout(), g.output, view, table{})
			}
			if len(view.Buckets) == 0 {
				cmd.Printf("nothing recorded for %s in the last %s\n", view.Subject, view.Window)
				return nil
			}

			cpu := make([]float64, 0, len(view.Buckets))
			memory := make([]float64, 0, len(view.Buckets))
			for _, b := range view.Buckets {
				cpu = append(cpu, b.CPUPeak)
				memory = append(memory, float64(b.MemoryPeak))
			}

			cpuAvg, cpuPeak := summarise(cpu)
			memAvg, memPeak := summarise(memory)

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s over %s, %d buckets\n\n", view.Subject, view.Window,
				len(view.Buckets))
			fmt.Fprintf(out, "cpu     %s  avg %s  peak %s\n",
				spark(cpu, 100), percent(cpuAvg), percent(cpuPeak))
			fmt.Fprintf(out, "memory  %s  avg %dMi  peak %dMi\n",
				spark(memory, float64(view.Buckets[len(view.Buckets)-1].MemoryMiB)),
				int(memAvg), int(memPeak))
			fmt.Fprintf(out, "\noldest %s   newest %s\n",
				shortStamp(view.Buckets[0].At),
				shortStamp(view.Buckets[len(view.Buckets)-1].At))
			return nil
		},
	}

	cmd.Flags().StringVar(&window, "window", "", "how far back to look, such as 15m or 6h")
	return cmd
}
