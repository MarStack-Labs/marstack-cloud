package cli

import (
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

type eventView struct {
	ID       int64  `json:"id"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	NodeID   string `json:"node_id"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type eventListView struct {
	Events []eventView `json:"events"`
}

var eventHeaders = []string{"WHEN", "SEVERITY", "KIND", "SUBJECT", "MESSAGE"}

func eventRow(e eventView) []string {
	return []string{
		e.At[:min(len(e.At), 19)],
		e.Severity,
		e.Kind,
		e.Subject,
		e.Message,
	}
}

func newEventCmd(g *globals) *cobra.Command {
	var (
		limit    int
		subject  string
		kind     string
		severity string
	)

	cmd := &cobra.Command{
		Use:     "event",
		Short:   "Show what the platform did on its own",
		Aliases: []string{"events"},
		Long: "Show what the platform did on its own.\n\n" +
			"The audit trail records calls somebody made. This records the things that happen\n" +
			"with no caller: a workload restarting, a backend leaving a balancer. It is a ring\n" +
			"of the most recent entries, not an archive.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := url.Values{}
			if limit > 0 {
				query.Set("limit", strconv.Itoa(limit))
			}
			for key, value := range map[string]string{
				"subject": subject, "kind": kind, "severity": severity,
			} {
				if value != "" {
					query.Set(key, value)
				}
			}

			path := "/v1/events"
			if len(query) > 0 {
				path += "?" + query.Encode()
			}

			var list eventListView
			if err := g.client().do(cmd.Context(), "GET", path, nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Events))
			for _, e := range list.Events {
				rows = append(rows, eventRow(e))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: eventHeaders, rows: rows})
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 0, "how many entries, newest first")
	cmd.Flags().StringVar(&subject, "subject", "", "only entries about this resource id")
	cmd.Flags().StringVar(&kind, "kind", "", "only entries of this kind, such as instance.restarted")
	cmd.Flags().StringVar(&severity, "severity", "", "info, warn, or error")

	return cmd
}
