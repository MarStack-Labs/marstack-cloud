package cli

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/marstack-labs/marstack-cloud/internal/runtime/console"
	"github.com/spf13/cobra"
)

const detachKey = 0x1d

func newInstanceConsoleCmd() *cobra.Command {
	var root string

	cmd := &cobra.Command{
		Use:   "console <id>",
		Short: "Attach to the serial console of an instance running on this node",
		Long: "Attach to the serial console of an instance running on this node.\n\n" +
			"This talks to the hypervisor directly, so it must run on the node that\n" +
			"holds the instance, as root. Press Ctrl-] to detach.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			socket, err := console.Find(root, args[0])
			if errors.Is(err, console.ErrNoConsole) {
				return fmt.Errorf(
					"no console for %s on this node: only vm and microvm expose one, "+
						"and the instance must be running here", args[0])
			}
			if err != nil {
				return err
			}
			return attachConsole(cmd, socket)
		},
	}

	cmd.Flags().StringVar(&root, "runtime-root", console.DefaultRoot,
		"directory the node agent keeps runtime state in")
	return cmd
}

func attachConsole(cmd *cobra.Command, socket string) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return fmt.Errorf("attach to the console: %w", err)
	}
	defer conn.Close()

	restore, raw := makeRaw(int(os.Stdin.Fd()))
	defer restore()

	if raw {
		cmd.PrintErrln("attached, press Ctrl-] to detach")
	} else {
		cmd.PrintErrln("attached in line mode, press Ctrl-C to detach")
	}

	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(cmd.OutOrStdout(), conn)
		done <- copyErr
	}()

	go func() {
		done <- forward(conn, os.Stdin)
	}()

	<-done
	cmd.PrintErrln("\r\ndetached")
	return nil
}

func forward(conn net.Conn, in io.Reader) error {
	buffer := make([]byte, 1024)
	for {
		read, err := in.Read(buffer)
		for index := 0; index < read; index++ {
			if buffer[index] == detachKey {
				if index > 0 {
					_, _ = conn.Write(buffer[:index])
				}
				return nil
			}
		}
		if read > 0 {
			if _, writeErr := conn.Write(buffer[:read]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			return err
		}
	}
}
