package cli

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/server"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

func newServerCmd() *cobra.Command {
	var (
		listen  string
		dataDir string
	)

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the control plane",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

			st, err := store.Open(ctx, dataDir)
			if err != nil {
				return err
			}
			defer st.Close()

			log.Info("store ready", "dir", dataDir)

			return server.New(server.Config{Listen: listen}, st, log).Run(ctx)
		},
	}

	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:7443", "address the control plane listens on")
	cmd.Flags().StringVar(&dataDir, "data-dir", "./data", "directory holding the control plane database")

	return cmd
}
