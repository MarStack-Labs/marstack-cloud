package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const maxTokenBytes = 4096

type globals struct {
	endpoint  string
	output    string
	token     string
	tokenFile string
}

func (g *globals) client() *client {
	return newClient(g.endpoint, g.secret())
}

func (g *globals) secret() string {
	if g.token != "" {
		return g.token
	}
	if v := os.Getenv(tokenEnvVar); v != "" {
		return strings.TrimSpace(v)
	}

	path := g.tokenFile
	if path == "" {
		path = os.Getenv(tokenFileEnvVar)
	}
	if path == "" {
		return ""
	}

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return ""
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxTokenBytes))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
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
	root.PersistentFlags().StringVar(&g.token, "token", "",
		"bearer token, overrides "+tokenEnvVar)
	root.PersistentFlags().StringVar(&g.tokenFile, "token-file", "",
		"file holding a bearer token, overrides "+tokenFileEnvVar)
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
		newTokenCmd(g),
		newNodeCmd(g),
	)
	return root
}

func Execute() error {
	return newRootCmd().Execute()
}
