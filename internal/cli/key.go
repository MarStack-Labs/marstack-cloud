package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type keyView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"`
	Comment     string `json:"comment,omitempty"`
	CreatedAt   string `json:"created_at"`
}

type keyListView struct {
	Keys []keyView `json:"keys"`
}

var keyHeaders = []string{"NAME", "TYPE", "FINGERPRINT", "COMMENT"}

func keyRow(k keyView) []string {
	return []string{k.Name, k.Kind, k.Fingerprint, k.Comment}
}

func newKeyCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage the ssh keys an instance can be created with",
		Long: "Manage the ssh keys an instance can be created with.\n\n" +
			"Keys belong to a project and are installed by cloud-init at first boot, so only\n" +
			"isolation vm can use them, and adding a key later does not reach an instance\n" +
			"that has already booted.",
		Aliases: []string{"keys"},
	}
	cmd.AddCommand(newKeyAddCmd(g), newKeyListCmd(g), newKeyDeleteCmd(g))
	return cmd
}

func newKeyAddCmd(g *globals) *cobra.Command {
	var (
		file string
		body struct {
			Name      string `json:"name"`
			PublicKey string `json:"public_key"`
		}
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a public key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := os.ReadFile(filepath.Clean(file))
			if err != nil {
				return fmt.Errorf("read the public key: %w", err)
			}
			body.PublicKey = strings.TrimSpace(string(raw))

			var created keyView
			if err := g.client().do(cmd.Context(), "POST", "/v1/keys", body, &created); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: keyHeaders,
				rows:    [][]string{keyRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&body.Name, "name", "", "a name to refer to the key by")
	cmd.Flags().StringVar(&file, "file", "", "path to a .pub file")
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("file"))

	return cmd
}

func newKeyListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the keys in this project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list keyListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/keys", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Keys))
			for _, k := range list.Keys {
				rows = append(rows, keyRow(k))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{
				headers: keyHeaders,
				rows:    rows,
			})
		},
	}
}

func newKeyDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Remove a key, leaving instances already booted with it alone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.client().do(cmd.Context(), "DELETE", "/v1/keys/"+args[0], nil, nil)
		},
	}
}
