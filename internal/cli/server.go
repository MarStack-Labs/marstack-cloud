package cli

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/app"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
)

func newServerCmd() *cobra.Command {
	var (
		listen   string
		dataDir  string
		logLevel string
		tlsCert  string
		tlsKey   string
	)

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the control plane",
		Long: "Run the control plane.\n\n" +
			"Without --tls-cert and --tls-key it serves plain HTTP, and every bearer token\n" +
			"crosses the network in the clear. That is fine on a loopback address and wrong\n" +
			"anywhere else, so it says so on every start.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			log := logging.New(logLevel, os.Stderr)

			if (tlsCert == "") != (tlsKey == "") {
				return errors.New("--tls-cert and --tls-key go together")
			}

			a, err := app.New(ctx, app.Config{
				Listen:  listen,
				DataDir: dataDir,
				TLSCert: tlsCert,
				TLSKey:  tlsKey,
			}, log)
			if err != nil {
				return err
			}
			defer a.Close()

			return a.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:7443", "address the control plane listens on")
	cmd.Flags().StringVar(&dataDir, "data-dir", "./data", "directory holding the control plane database")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "PEM certificate chain to serve HTTPS with")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "PEM private key for --tls-cert")

	return cmd
}
