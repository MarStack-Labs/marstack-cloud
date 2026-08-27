package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

type tokenView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	ProjectID  string `json:"project_id"`
	Secret     string `json:"secret,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	Expired    bool   `json:"expired,omitempty"`
}

type tokenListView struct {
	Tokens []tokenView `json:"tokens"`
}

var tokenHeaders = []string{"NAME", "ID", "ROLE", "PROJECT", "EXPIRES", "LAST USED"}

func tokenRow(t tokenView) []string {
	used := t.LastUsedAt
	if len(used) > 19 {
		used = used[:19]
	}
	expiry := "never"
	if t.ExpiresAt != "" {
		expiry = t.ExpiresAt[:min(19, len(t.ExpiresAt))]
	}
	if t.Expired {
		expiry += " (expired)"
	}
	return []string{t.Name, t.ID, t.Role, t.ProjectID, expiry, used}
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
		Name      string `json:"name"`
		Role      string `json:"role"`
		ProjectID string `json:"project_id,omitempty"`
		ExpiresIn string `json:"expires_in,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a token, printing the secret once",
		Long: "Create a token, printing the secret once.\n\n" +
			"Role admin can call everything in its project, and administers projects, tokens\n" +
			"and the audit trail. Role member manages resources in its project only. Role node\n" +
			"can only call the endpoints an agent needs, so a compromised node cannot schedule\n" +
			"work or read the whole platform.\n\n" +
			"A token without --project lands in the project of the token that created it, and a\n" +
			"token without --ttl never expires.",
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
	cmd.Flags().StringVar(&req.Role, "role", "admin", "admin, member or node")
	cmd.Flags().StringVar(&req.ProjectID, "project", "", "project the token works in")
	cmd.Flags().StringVar(&req.ExpiresIn, "ttl", "",
		"how long the token stays valid, such as 12h, 30d or 4w; empty means never expires")
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
