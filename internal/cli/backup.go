package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
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
	cmd.AddCommand(newBackupCreateCmd(g), newBackupListCmd(g), newBackupDeleteCmd(g),
		newScheduleCmd(g), newBackupKeygenCmd(g))
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

type scheduleView struct {
	ID       string `json:"id"`
	VolumeID string `json:"volume_id"`
	Every    string `json:"every"`
	Keep     int    `json:"keep"`
	NextAt   string `json:"next_at"`
	LastAt   string `json:"last_at,omitempty"`
}

type scheduleListView struct {
	Schedules []scheduleView `json:"schedules"`
}

var scheduleHeaders = []string{"VOLUME", "EVERY", "KEEP", "NEXT", "LAST"}

func scheduleRow(sc scheduleView) []string {
	last := sc.LastAt
	if last == "" {
		last = "never"
	}
	return []string{sc.VolumeID, sc.Every, strconv.Itoa(sc.Keep),
		shortStamp(sc.NextAt), shortStamp(last)}
}

func newScheduleCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Take backups on a timer and keep only the newest",
		Long: "Take backups on a timer and keep only the newest.\n\n" +
			"A volume carries at most one schedule. Retention only ever removes copies the\n" +
			"schedule itself made, so a backup you took by hand is never pruned.",
		Aliases: []string{"schedules"},
	}
	cmd.AddCommand(newScheduleSetCmd(g), newScheduleListCmd(g), newScheduleClearCmd(g))
	return cmd
}

func newScheduleSetCmd(g *globals) *cobra.Command {
	var req struct {
		Every string `json:"every"`
		Keep  int    `json:"keep"`
	}

	cmd := &cobra.Command{
		Use:   "set <volume>",
		Short: "Set or replace the backup schedule of a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sc scheduleView
			err := g.client().do(cmd.Context(), "PUT", "/v1/volumes/"+args[0]+"/schedule", req, &sc)
			if err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, sc, table{
				headers: scheduleHeaders,
				rows:    [][]string{scheduleRow(sc)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Every, "every", "", "how often, such as 6h, 1d or 1w")
	cmd.Flags().IntVar(&req.Keep, "keep", 7, "how many of its own copies to keep")
	must(cmd.MarkFlagRequired("every"))

	return cmd
}

func newScheduleListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List backup schedules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list scheduleListView
			err := g.client().do(cmd.Context(), "GET", "/v1/backup-schedules", nil, &list)
			if err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Schedules))
			for _, sc := range list.Schedules {
				rows = append(rows, scheduleRow(sc))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{
				headers: scheduleHeaders,
				rows:    rows,
			})
		},
	}
}

func newScheduleClearCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <volume>",
		Short: "Stop taking scheduled backups of a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.client().do(cmd.Context(), "DELETE",
				"/v1/volumes/"+args[0]+"/schedule", nil, nil)
		},
	}
}

func newBackupKeygenCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Print a new backup key",
		Long: "Print a new backup key.\n\n" +
			"Write it to a file and pass that file to marstack server --backup-key-file.\n" +
			"Keep it somewhere other than the data directory it protects: a key stored next\n" +
			"to the backups it seals protects nothing. Lose it and those backups are gone.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, text := sealed.NewKey()

			if g.output != outputTable {
				return render(cmd.OutOrStdout(), g.output,
					map[string]string{"key": text, "key_id": key.ID()}, table{})
			}

			if _, err := fmt.Fprintln(cmd.OutOrStdout(), text); err != nil {
				return err
			}
			cmd.PrintErrln("key " + key.ID() + " — this is the only time it is shown")
			return nil
		},
	}
}
