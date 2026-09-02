package cli

import (
	"errors"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

type execView struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id"`
	Command    []string `json:"command"`
	State      string   `json:"state"`
	Output     string   `json:"output"`
	Truncated  bool     `json:"truncated"`
	ExitCode   *int     `json:"exit_code"`
	Message    string   `json:"message"`
	CreatedAt  string   `json:"created_at"`
}

type execListView struct {
	Execs []execView `json:"execs"`
}

var execHeaders = []string{"ID", "INSTANCE", "STATE", "EXIT", "STARTED", "COMMAND"}

func execRow(e execView) []string {
	exit := "-"
	if e.ExitCode != nil {
		exit = strconv.Itoa(*e.ExitCode)
	}
	return []string{
		e.ID,
		e.InstanceID,
		e.State,
		exit,
		e.CreatedAt[:min(len(e.CreatedAt), 19)],
		shortenLine(joinCommand(e.Command), 34),
	}
}

func joinCommand(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += " "
		}
		out += part
	}
	return out
}

func newExecCmd(g *globals) *cobra.Command {
	var (
		timeout string
		wait    time.Duration
	)

	cmd := &cobra.Command{
		Use:   "exec <instance> -- command args...",
		Short: "Run one command inside a running container and show what it printed",
		Long: "Run one command inside a running container and show what it printed.\n\n" +
			"This is not a shell. There is no stdin and no terminal: the command runs, its\n" +
			"output is captured, and you get the output and the exit code. An interactive\n" +
			"session needs a channel that stays open between here and the node, and the\n" +
			"node only ever calls out - so that is a different thing, not a flag on this.\n\n" +
			"Only a container can be entered. A vm, microvm or sandbox runs its own kernel,\n" +
			"so getting inside one needs something running in the guest; marstack console on\n" +
			"the node attaches to a vm's serial port instead.\n\n" +
			"The node picks the command up on its next pass, so expect a few seconds before\n" +
			"anything happens.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := struct {
				Command []string `json:"command"`
				Timeout string   `json:"timeout,omitempty"`
			}{Command: args[1:], Timeout: timeout}

			var started execView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/instances/"+args[0]+"/exec", body, &started,
			); err != nil {
				return err
			}

			finished, err := waitForCommand(cmd, g, started.ID, wait)
			if err != nil {
				return err
			}

			if g.output == "json" {
				return render(cmd.OutOrStdout(), g.output, finished, table{})
			}
			if finished.Truncated {
				cmd.PrintErrln("the start of the output was dropped")
			}
			if finished.Output != "" {
				if _, err := cmd.OutOrStdout().Write([]byte(finished.Output)); err != nil {
					return err
				}
			}
			if finished.Message != "" {
				cmd.PrintErrln(finished.Message)
			}
			if finished.ExitCode != nil && *finished.ExitCode != 0 {
				return errors.New("the command exited with " +
					strconv.Itoa(*finished.ExitCode))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&timeout, "timeout", "",
		"how long the command may run inside, such as 10s or 2m")
	cmd.Flags().DurationVar(&wait, "wait", 2*time.Minute,
		"how long to wait for a node to pick it up and answer")
	cmd.AddCommand(newExecListCmd(g))

	return cmd
}

func waitForCommand(cmd *cobra.Command, g *globals, id string,
	wait time.Duration) (execView, error) {
	deadline := time.Now().Add(wait)

	for {
		var held execView
		if err := g.client().do(
			cmd.Context(), "GET", "/v1/execs/"+id, nil, &held,
		); err != nil {
			return execView{}, err
		}

		switch held.State {
		case "done":
			return held, nil
		case "lost":
			return execView{}, errors.New("no node picked the command up: " + held.Message)
		}

		if time.Now().After(deadline) {
			return execView{}, errors.New("the command is still " + held.State +
				" after " + wait.String() + ": read it later with marstack exec list")
		}

		select {
		case <-cmd.Context().Done():
			return execView{}, cmd.Context().Err()
		case <-time.After(time.Second):
		}
	}
}

func newExecListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the commands that have been run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list execListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/execs", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Execs))
			for _, one := range list.Execs {
				rows = append(rows, execRow(one))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: execHeaders, rows: rows})
		},
	}
}
