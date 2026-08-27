package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/app"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/logging"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

func newServerCmd() *cobra.Command {
	var (
		listen   string
		dataDir  string
		logLevel string
		tlsCert  string
		tlsKey   string
		keyFiles []string
	)

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the control plane",
		Long: "Run the control plane.\n\n" +
			"Without --tls-cert and --tls-key it serves plain HTTP, and every bearer token\n" +
			"crosses the network in the clear. That is fine on a loopback address and wrong\n" +
			"anywhere else, so it says so on every start.\n\n" +
			"Without --backup-key-file, backup content is stored unencrypted. Lose a key and\n" +
			"the backups it sealed are gone for good, so keep it somewhere other than the\n" +
			"data directory it protects.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			log := logging.New(logLevel, os.Stderr)

			if (tlsCert == "") != (tlsKey == "") {
				return errors.New("--tls-cert and --tls-key go together")
			}

			keys, err := readBackupKeys(keyFiles)
			if err != nil {
				return err
			}

			a, err := app.New(ctx, app.Config{
				Listen:     listen,
				DataDir:    dataDir,
				TLSCert:    tlsCert,
				TLSKey:     tlsKey,
				BackupKeys: keys,
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
	cmd.Flags().StringArrayVar(&keyFiles, "backup-key-file", nil,
		"file holding 64 hex characters; repeat to keep reading older backups, "+
			"the first seals new ones")

	return cmd
}

func readBackupKeys(paths []string) ([]sealed.Key, error) {
	keys := make([]sealed.Key, 0, len(paths))
	seen := map[string]bool{}

	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("read the backup key: %w", err)
		}

		key, err := sealed.ParseKey(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if seen[key.ID()] {
			return nil, fmt.Errorf("%s holds a key already given", path)
		}

		seen[key.ID()] = true
		keys = append(keys, key)
	}
	return keys, nil
}
