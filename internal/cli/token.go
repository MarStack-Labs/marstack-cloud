package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

type tokenView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Secret     string `json:"secret,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
}

type tokenListView struct {
	Tokens []tokenView `json:"tokens"`
}

var tokenHeaders = []string{"NAME", "ID", "ROLE", "LAST USED"}

func tokenRow(t tokenView) []string {
	used := t.LastUsedAt
	if len(used) > 19 {
		used = used[:19]
	}
	return []string{t.Name, t.ID, t.Role, used}
}

func newTokenCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "token",
		Short:   "Manage API tokens",
		Aliases: []string{"tokens"},
	}
	cmd.AddCommand(newTokenCreateCmd(g), newTokenListCmd(g), newTokenRevokeCmd(g))
	return cmd
}

func newTokenCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a token, printing the secret once",
		Long: "Create a token, printing the secret once.\n\n" +
			"Role admin can call everything. Role node can only call the endpoints an agent\n" +
			"needs, so a compromised node cannot schedule work or read the whole platform.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created tokenView
			if err := g.client().do(cmd.Context(), "POST", "/v1/tokens", req, &created); err != nil {
				return err
			}

			if g.output == "table" {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), created.Secret); err != nil {
					return err
				}
				cmd.PrintErrln("this secret is not stored anywhere else")
				return nil
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: tokenHeaders,
				rows:    [][]string{tokenRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "token name, unique within the platform")
	cmd.Flags().StringVar(&req.Role, "role", "admin", "admin or node")
	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newTokenListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List tokens without their secrets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list tokenListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/tokens", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Tokens))
			for _, t := range list.Tokens {
				rows = append(rows, tokenRow(t))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: tokenHeaders, rows: rows})
		},
	}
}

func newTokenRevokeCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/tokens/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("revoked %s\n", args[0])
			return nil
		},
	}
}
