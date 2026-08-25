package cli

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/agent"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

func newAgentCmd(g *globals) *cobra.Command {
	var (
		name     string
		zone     string
		interval time.Duration
		logLevel string
	)

	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the node agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if name == "" {
				hostname, err := os.Hostname()
				if err != nil {
					return err
				}
				name = hostname
			}

			log := logging.New(logLevel, os.Stderr)

			return agent.New(agent.Config{
				Endpoint: g.endpoint,
				Name:     name,
				Zone:     zone,
				Interval: interval,
			}, log).Run(ctx)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "node name, defaults to the hostname")
	cmd.Flags().StringVar(&zone, "zone", "", "failure domain this node belongs to")
	cmd.Flags().DurationVar(&interval, "interval", agent.DefaultInterval, "heartbeat interval")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")

	return cmd
}
