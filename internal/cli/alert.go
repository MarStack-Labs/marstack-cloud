package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type alertView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	InstanceID string  `json:"instance_id"`
	Metric     string  `json:"metric"`
	Comparison string  `json:"comparison"`
	Threshold  float64 `json:"threshold"`
	For        string  `json:"for"`
	State      string  `json:"state"`
	LastValue  float64 `json:"last_value"`
	Message    string  `json:"message"`
}

type alertListView struct {
	Alerts []alertView `json:"alerts"`
}

var alertHeaders = []string{"NAME", "INSTANCE", "WATCHES", "STATE", "NOW", "WHY"}

func alertRow(a alertView) []string {
	return []string{
		a.Name,
		a.InstanceID,
		a.Metric + " " + a.Comparison + " " +
			strconv.FormatFloat(a.Threshold, 'f', -1, 64) + "% for " + a.For,
		a.State,
		strconv.FormatFloat(a.LastValue, 'f', 1, 64) + "%",
		shortenLine(a.Message, 40),
	}
}

func newAlertCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alert",
		Short: "Watch a workload's load and record when it crosses a line",
		Long: "Watch a workload's load and record when it crosses a line.\n\n" +
			"An alert fires only once the reading has held for the whole window, so a single\n" +
			"spike is not an alert. It fires once and clears once: the event is written when\n" +
			"the state changes, never on every pass, so a webhook is not woken every twenty\n" +
			"seconds for something it already knows.\n\n" +
			"A reading that is missing or stale stops the alert rather than counting as zero.\n" +
			"A workload that stopped reporting is not a workload that went quiet.",
	}
	cmd.AddCommand(newAlertCreateCmd(g), newAlertListCmd(g), newAlertDeleteCmd(g))
	return cmd
}

func newAlertCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name       string  `json:"name"`
		InstanceID string  `json:"instance_id"`
		Metric     string  `json:"metric"`
		Comparison string  `json:"comparison,omitempty"`
		Threshold  float64 `json:"threshold"`
		For        string  `json:"for,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Watch one workload",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created alertView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/alerts", req, &created,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created,
				table{headers: alertHeaders, rows: [][]string{alertRow(created)}})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "alert name, unique in the project")
	cmd.Flags().StringVar(&req.InstanceID, "instance", "", "workload to watch, by name or id")
	cmd.Flags().StringVar(&req.Metric, "metric", "cpu", "cpu or memory")
	cmd.Flags().StringVar(&req.Comparison, "comparison", "above", "above or below")
	cmd.Flags().Float64Var(&req.Threshold, "threshold", 0, "percentage to cross")
	cmd.Flags().StringVar(&req.For, "for", "",
		"how long it must hold before firing, such as 5m; defaults to 2m")
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("instance"))
	must(cmd.MarkFlagRequired("threshold"))
	return cmd
}

func newAlertListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the alerts and what they read last",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list alertListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/alerts", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Alerts))
			for _, one := range list.Alerts {
				rows = append(rows, alertRow(one))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: alertHeaders, rows: rows})
		},
	}
}

func newAlertDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Stop watching",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/alerts/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
