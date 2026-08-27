package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type auditEntryView struct {
	At        string `json:"at"`
	Actor     string `json:"actor"`
	Role      string `json:"role"`
	ProjectID string `json:"project_id,omitempty"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	RequestID string `json:"request_id,omitempty"`
}

type auditListView struct {
	Entries []auditEntryView `json:"entries"`
}

var auditHeaders = []string{"WHEN", "ACTOR", "ROLE", "PROJECT", "METHOD", "PATH", "STATUS"}

func auditRow(e auditEntryView) []string {
	actor := e.Actor
	if actor == "" {
		actor = "(unproven)"
	}
	role := e.Role
	if role == "" {
		role = "-"
	}
	project := e.ProjectID
	if project == "" {
		project = "-"
	}
	return []string{shortStamp(e.At), actor, role, project, e.Method, e.Path,
		strconv.Itoa(e.Status)}
}

func newAuditCmd(g *globals) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show who changed what, and who was refused",
		Long: "Show who changed what, and who was refused.\n\n" +
			"Reads are not recorded and request bodies are never stored, so a token secret\n" +
			"cannot leak into the trail. Only the most recent entries are kept.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/v1/audit"
			if limit > 0 {
				path += "?limit=" + strconv.Itoa(limit)
			}

			var list auditListView
			if err := g.client().do(cmd.Context(), "GET", path, nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Entries))
			for _, entry := range list.Entries {
				rows = append(rows, auditRow(entry))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: auditHeaders, rows: rows})
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 0, "how many entries to show, newest first")
	return cmd
}
