package cli

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/agent"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/catalog"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/container"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/microvm"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/netdev"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/qemu"
	"github.com/marstack-labs/marstack-cloud/internal/runtime/resolver"
	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func newAgentCmd(g *globals) *cobra.Command {
	var (
		name        string
		zone        string
		address     string
		stateDir    string
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
			images := catalog.New(filepath.Join(runtimeRoot, "images"), log)

			return agent.New(agent.Config{
				Endpoint: g.endpoint,
				Token:    g.secret(),
				Name:     name,
				Zone:     zone,
				Address:  address,
				StateDir: stateDir,
				Interval: interval,
			}, agent.Deps{
				Runtimes: map[string]workload.Runtime{
					"container": container.New(runtimeRoot, log),
					"vm":        qemu.New(runtimeRoot, log, images),
					"microvm":   microvm.New(runtimeRoot, log, microvm.CloudHypervisor(), images),
					"sandbox":   microvm.New(runtimeRoot, log, microvm.Firecracker(), images),
				},
				Datapath: netdev.Datapath{},
				Resolver: resolver.New(log),
				Catalog:  images,
			}, log).Run(ctx)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "node name, defaults to the hostname")
	cmd.Flags().StringVar(&zone, "zone", "", "failure domain this node belongs to")
	cmd.Flags().StringVar(&address, "address", "",
		"address the other nodes reach this one on, required for multi-node routing")
	cmd.Flags().StringVar(&stateDir, "state-dir", container.DefaultRoot+"/agent",
		"directory where the agent keeps the last known desired state")
	cmd.Flags().StringVar(&runtimeRoot, "runtime-root", container.DefaultRoot,
		"directory holding images and instance state on this node")
	cmd.Flags().DurationVar(&interval, "interval", agent.DefaultInterval, "heartbeat and reconcile interval")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")

	return cmd
}
