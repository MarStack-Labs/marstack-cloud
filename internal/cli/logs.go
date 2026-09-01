package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type logLineView struct {
	Seq        int64  `json:"seq"`
	Text       string `json:"text"`
	ReceivedAt string `json:"received_at"`
}

type logTailView struct {
	InstanceID string        `json:"instance_id"`
	Lines      []logLineView `json:"lines"`
	Truncated  bool          `json:"truncated"`
}

func newLogsCmd(g *globals) *cobra.Command {
	var tail int

	cmd := &cobra.Command{
		Use:   "logs <instance-id>",
		Short: "Show what a workload printed",
		Long: "Show what a workload printed.\n\n" +
			"The node ships new output to the control plane on every reconcile pass, so the\n" +
			"last line can be a few seconds behind. Only a recent window is kept: the oldest\n" +
			"lines are dropped once an instance has more than the platform holds, and nothing\n" +
			"survives the instance being deleted. This is for looking at why something is not\n" +
			"working, not for keeping records - ship them somewhere else for that.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/instances/" + args[0] + "/logs"
			if tail > 0 {
				path += "?tail=" + strconv.Itoa(tail)
			}

			var body logTailView
			if err := g.client().do(cmd.Context(), "GET", path, nil, &body); err != nil {
				return err
			}

			if g.output == "json" {
				return render(cmd.OutOrStdout(), g.output, body, table{})
			}

			out := cmd.OutOrStdout()
			if body.Truncated {
				cmd.PrintErrln("the oldest lines have been dropped")
			}
			for _, line := range body.Lines {
				if _, err := out.Write([]byte(line.Text + "\n")); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&tail, "tail", 0, "how many of the most recent lines to show")
	return cmd
}
