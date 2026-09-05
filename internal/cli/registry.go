package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type registryView struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Username  string `json:"username"`
	KeyID     string `json:"key_id"`
	CreatedAt string `json:"created_at"`
}

type registryListView struct {
	Credentials []registryView `json:"credentials"`
}

var registryHeaders = []string{"HOST", "USER", "ID", "SEALED WITH", "ADDED"}

func registryRow(c registryView) []string {
	return []string{
		c.Host,
		c.Username,
		c.ID,
		c.KeyID,
		c.CreatedAt[:min(len(c.CreatedAt), 19)],
	}
}

func newRegistryCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Log in to a private image registry",
		Long: "Log in to a private image registry.\n\n" +
			"Without a credential a node can only pull what a registry serves anonymously,\n" +
			"which means no private image at all and a much lower rate limit on the public\n" +
			"ones. A credential is held per host and used by every node.\n\n" +
			"The password is sealed with the operator key and served only to nodes. It is\n" +
			"never returned to an operator, so there is nothing to read back: replace it by\n" +
			"deleting the credential and adding it again.",
	}
	cmd.AddCommand(newRegistryAddCmd(g), newRegistryListCmd(g), newRegistryRemoveCmd(g))
	return cmd
}

func newRegistryAddCmd(g *globals) *cobra.Command {
	var (
		host         string
		username     string
		passwordFile string
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a login for one registry host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := os.ReadFile(passwordFile)
			if err != nil {
				return fmt.Errorf("read %s: %w", passwordFile, err)
			}

			body := struct {
				Host     string `json:"host"`
				Username string `json:"username"`
				Password string `json:"password"`
			}{
				Host:     host,
				Username: username,
				Password: strings.TrimRight(string(raw), "\r\n"),
			}

			var created registryView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/registries", body, &created,
			); err != nil {
				return err
			}

			cmd.PrintErrln("nodes pick it up on their next pass")
			return render(cmd.OutOrStdout(), g.output, created,
				table{headers: registryHeaders, rows: [][]string{registryRow(created)}})
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "registry host, such as ghcr.io or registry-1.docker.io")
	cmd.Flags().StringVar(&username, "username", "", "user this credential logs in as")
	cmd.Flags().StringVar(&passwordFile, "password-file", "",
		"file holding the password or token; a flag would be kept in shell history and shown in ps")
	must(cmd.MarkFlagRequired("host"))
	must(cmd.MarkFlagRequired("username"))
	must(cmd.MarkFlagRequired("password-file"))
	return cmd
}

func newRegistryListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the registries this platform can log in to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list registryListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/registries", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Credentials))
			for _, one := range list.Credentials {
				rows = append(rows, registryRow(one))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: registryHeaders, rows: rows})
		},
	}
}

func newRegistryRemoveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <host|id>",
		Short: "Forget a registry login",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/registries/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("removed %s\n", args[0])
			return nil
		},
	}
}
