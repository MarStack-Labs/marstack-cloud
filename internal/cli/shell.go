package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

type shellView struct {
	ID         string   `json:"id"`
	InstanceID string   `json:"instance_id"`
	Command    []string `json:"command"`
	State      string   `json:"state"`
	Message    string   `json:"message"`
	CreatedAt  string   `json:"created_at"`
}

type shellListView struct {
	Sessions []shellView `json:"sessions"`
}

var shellHeaders = []string{"ID", "INSTANCE", "STATE", "COMMAND", "STARTED", "WHY"}

func shellRow(s shellView) []string {
	return []string{
		s.ID,
		s.InstanceID,
		s.State,
		joinCommand(s.Command),
		s.CreatedAt[:min(len(s.CreatedAt), 19)],
		shortenLine(s.Message, 30),
	}
}

func newShellCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell <instance> [-- command args...]",
		Short: "Open a shell inside a running container and stay attached",
		Long: "Open a shell inside a running container and stay attached.\n\n" +
			"Unlike marstack exec, which runs one command and hands back its output, this\n" +
			"holds the connection open: what you type goes down and what the shell prints\n" +
			"comes back, until you press ctrl-] or the shell exits.\n\n" +
			"The node has no listener, so it is the node that dials out: it takes the\n" +
			"session, opens one request to read your keystrokes and another to send the\n" +
			"output back, and the control plane joins the two.\n\n" +
			"This is not a terminal. There is no pty on the far side, so there is no prompt,\n" +
			"no line editing and no ctrl-c: a command runs and its output comes back. Say\n" +
			"exit, or press ctrl-], to leave.\n\n" +
			"Only a container can be entered. A vm, microvm or sandbox runs its own kernel.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := struct {
				Command []string `json:"command,omitempty"`
			}{Command: args[1:]}

			var opened shellView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/instances/"+args[0]+"/shell", body, &opened,
			); err != nil {
				return err
			}

			cmd.PrintErrln("attached to " + opened.ID + "; ctrl-] to leave")
			return attachShell(cmd.Context(), g, opened.ID, cmd.OutOrStdout())
		},
	}

	cmd.AddCommand(newShellListCmd(g))
	return cmd
}

func attachShell(ctx context.Context, g *globals, id string, out io.Writer) error {
	inner, stop := context.WithCancel(ctx)
	defer stop()

	restore, raw := makeRaw(int(os.Stdin.Fd()))
	defer restore()

	go func() {
		left, err := pumpShellInput(inner, g, id, raw)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stderr, "input stopped:", err)
		}
		if left {
			stop()
		}
	}()

	stream, err := g.client().open(inner, "GET", "/v1/shells/"+id+"/output", nil)
	if err != nil {
		return err
	}
	defer stream.Close()

	_, err = io.Copy(out, stream)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func pumpShellInput(
	ctx context.Context, g *globals, id string, raw bool,
) (bool, error) {
	buffer := make([]byte, 1024)

	for {
		read, err := os.Stdin.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			if raw {
				if at := bytes.IndexByte(chunk, 0x1d); at >= 0 {
					chunk = chunk[:at]
					if len(chunk) > 0 {
						_ = sendShellInput(ctx, g, id, chunk)
					}
					return true, nil
				}
			}
			if err := sendShellInput(ctx, g, id, chunk); err != nil {
				return false, err
			}
		}
		if err != nil {
			return false, err
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
	}
}

func sendShellInput(ctx context.Context, g *globals, id string, chunk []byte) error {
	stream, err := g.client().open(ctx, http.MethodPost, "/v1/shells/"+id+"/input",
		bytes.NewReader(chunk))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, stream)
	return stream.Close()
}

func newShellListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the shell sessions this project has opened",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list shellListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/shells", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Sessions))
			for _, one := range list.Sessions {
				rows = append(rows, shellRow(one))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: shellHeaders, rows: rows})
		},
	}
}
