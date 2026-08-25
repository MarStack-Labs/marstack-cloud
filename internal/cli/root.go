package cli

import "github.com/spf13/cobra"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "marstack",
		Short:         "MarStack Cloud control plane and node agent",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newVersionCmd(),
		newServerCmd(),
	)
	return root
}

func Execute() error {
	return newRootCmd().Execute()
}
