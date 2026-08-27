package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type backupView struct {
	ID        string `json:"id"`
	VolumeID  string `json:"volume_id"`
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

type backupListView struct {
	Backups []backupView `json:"backups"`
}

var backupHeaders = []string{"NAME", "ID", "VOLUME", "STATE", "SIZE", "CREATED"}

func backupRow(b backupView) []string {
	created := b.CreatedAt
	if len(created) > 19 {
		created = created[:19]
	}

	state := b.State
	if b.Message != "" {
		state += " (" + b.Message + ")"
	}
	return []string{b.Name, b.ID, b.VolumeID, state, strconv.FormatInt(b.SizeBytes, 10), created}
}

func newBackupCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage volume backups",
		Long: "Manage volume backups.\n\n" +
			"A snapshot lives inside the volume file on the node that holds it, so it\n" +
			"survives a bad write but not a lost node. A backup copies the volume off the\n" +
			"node to the control plane, and a new volume can be created from it with\n" +
			"marstack volume create --from-backup.",
		Aliases: []string{"backups"},
	}
	cmd.AddCommand(newBackupCreateCmd(g), newBackupListCmd(g), newBackupDeleteCmd(g))
	return cmd
}

func newBackupCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name string `json:"name"`
	}

	cmd := &cobra.Command{
		Use:   "create <volume>",
		Short: "Ask the node holding a volume to copy it off the node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var created backupView
			err := g.client().do(cmd.Context(), "POST", "/v1/volumes/"+args[0]+"/backups",
				req, &created)
			if err != nil {
				return err
			}

			cmd.PrintErrln("the node copies the volume on its next reconcile; " +
				"watch the state with marstack backup list")
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: backupHeaders,
				rows:    [][]string{backupRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "backup name, unique for that volume")
	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newBackupListCmd(g *globals) *cobra.Command {
	var volume string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/v1/backups"
			if volume != "" {
				path = "/v1/volumes/" + volume + "/backups"
			}

			var list backupListView
			if err := g.client().do(cmd.Context(), "GET", path, nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Backups))
			for _, b := range list.Backups {
				rows = append(rows, backupRow(b))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{
				headers: backupHeaders,
				rows:    rows,
			})
		},
	}

	cmd.Flags().StringVar(&volume, "volume", "", "only backups of this volume")
	return cmd
}

func newBackupDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a backup and the bytes it holds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.client().do(cmd.Context(), "DELETE", "/v1/backups/"+args[0], nil, nil)
		},
	}
}
