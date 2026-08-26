package cli

import (
	"os"

	"github.com/spf13/cobra"
)

type globals struct {
	endpoint string
	output   string
}

func resolveDefaultEndpoint() string {
	if v := os.Getenv(endpointEnvVar); v != "" {
		return v
	}
	return defaultEndpoint
}

func newRootCmd() *cobra.Command {
	g := &globals{}

	root := &cobra.Command{
		Use:           "marstack",
		Short:         "MarStack Cloud control plane, node agent, and client",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&g.endpoint, "endpoint", resolveDefaultEndpoint(),
		"control plane endpoint, overrides "+endpointEnvVar)
	root.PersistentFlags().StringVarP(&g.output, "output", "o", outputTable,
		"output format: table, json")

	root.AddCommand(
		newVersionCmd(),
		newServerCmd(),
		newAgentCmd(g),
		newInstanceCmd(g),
		newNetworkCmd(g),
		newImageCmd(g),
		newVolumeCmd(g),
		newNodeCmd(g),
	)
	return root
}

func Execute() error {
	return newRootCmd().Execute()
}
