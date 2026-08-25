package cli

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/agent"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/container"
)

func newAgentCmd(g *globals) *cobra.Command {
	var (
		name        string
		zone        string
		runtimeRoot string
		interval    time.Duration
		logLevel    string
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
			}, container.New(runtimeRoot, log), log).Run(ctx)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "node name, defaults to the hostname")
	cmd.Flags().StringVar(&zone, "zone", "", "failure domain this node belongs to")
	cmd.Flags().StringVar(&runtimeRoot, "runtime-root", container.DefaultRoot,
		"directory holding images and instance state on this node")
	cmd.Flags().DurationVar(&interval, "interval", agent.DefaultInterval, "heartbeat and reconcile interval")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")

	return cmd
}

func newContainerInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "container-init",
		Short:  "Internal: become the init process of a container",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return container.RunInit()
		},
	}
}
