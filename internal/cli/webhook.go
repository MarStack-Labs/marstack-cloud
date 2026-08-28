package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type webhookView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Kinds     []string `json:"kinds"`
	Active    bool     `json:"active"`
	Secret    string   `json:"secret"`
	CreatedAt string   `json:"created_at"`
}

type webhookListView struct {
	Webhooks []webhookView `json:"webhooks"`
}

type webhookDeliveryView struct {
	ID            string `json:"id"`
	EventID       int64  `json:"event_id"`
	Kind          string `json:"kind"`
	Subject       string `json:"subject"`
	State         string `json:"state"`
	Attempts      int    `json:"attempts"`
	LastError     string `json:"last_error"`
	NextAttemptAt string `json:"next_attempt_at"`
	CreatedAt     string `json:"created_at"`
}

type webhookDeliveryListView struct {
	Deliveries []webhookDeliveryView `json:"deliveries"`
}

var webhookHeaders = []string{"NAME", "ID", "STATE", "KINDS", "URL"}

func webhookRow(w webhookView) []string {
	state := "active"
	if !w.Active {
		state = "paused"
	}

	kinds := "everything"
	if len(w.Kinds) > 0 {
		kinds = strings.Join(w.Kinds, ",")
	}

	return []string{w.Name, w.ID, state, kinds, shortenLine(w.URL, 50)}
}

func newWebhookCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webhook",
		Short:   "Send events to somewhere that can act on them",
		Aliases: []string{"webhooks"},
	}
	cmd.AddCommand(
		newWebhookCreateCmd(g),
		newWebhookListCmd(g),
		newWebhookDeliveriesCmd(g),
		newWebhookPauseCmd(g),
		newWebhookResumeCmd(g),
		newWebhookDeleteCmd(g),
	)
	return cmd
}

func newWebhookCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name  string   `json:"name"`
		URL   string   `json:"url"`
		Kinds []string `json:"kinds,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Subscribe an endpoint to this project's events",
		Long: "Subscribe an endpoint to this project's events.\n\n" +
			"Each delivery is a POST carrying the event kind and subject, signed with an\n" +
			"HMAC-SHA256 of the body in the Marstack-Signature header. The signing secret is\n" +
			"shown once, here, and never again.\n\n" +
			"A failed delivery is retried with a growing backoff and then given up on, so a\n" +
			"target that is briefly down loses nothing and a target that is gone does not\n" +
			"queue forever. Loopback and link-local addresses are refused at the dial: the\n" +
			"control plane will not be used to reach itself or a metadata service.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created webhookView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/webhooks", req, &created,
			); err != nil {
				return err
			}

			if err := render(cmd.OutOrStdout(), g.output, created, table{
				headers: webhookHeaders,
				rows:    [][]string{webhookRow(created)},
			}); err != nil {
				return err
			}
			if g.output != "json" {
				cmd.Printf("\nsigning secret: %s\n", created.Secret)
				cmd.Printf("this is the only time it is shown\n")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "webhook name, unique in the project")
	cmd.Flags().StringVar(&req.URL, "url", "", "where to post, http or https")
	cmd.Flags().StringSliceVar(&req.Kinds, "kind", nil,
		"only these kinds, repeatable, and instance.* matches a family")
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("url"))

	return cmd
}

func newWebhookListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List webhooks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list webhookListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/webhooks", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Webhooks))
			for _, w := range list.Webhooks {
				rows = append(rows, webhookRow(w))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: webhookHeaders, rows: rows})
		},
	}
}

var webhookDeliveryHeaders = []string{"WHEN", "KIND", "STATE", "TRIES", "WHY"}

func newWebhookDeliveriesCmd(g *globals) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "deliveries <name|id>",
		Short: "Show what was sent and what came back",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/webhooks/" + args[0] + "/deliveries"
			if limit > 0 {
				path += "?limit=" + strconv.Itoa(limit)
			}

			var list webhookDeliveryListView
			if err := g.client().do(cmd.Context(), "GET", path, nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Deliveries))
			for _, d := range list.Deliveries {
				why := d.LastError
				if why == "" {
					why = "-"
				}
				rows = append(rows, []string{
					shortStamp(d.CreatedAt), d.Kind, d.State,
					strconv.Itoa(d.Attempts), shortenLine(why, 60),
				})
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: webhookDeliveryHeaders, rows: rows})
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 0, "how many, newest first")
	return cmd
}

func webhookAction(g *globals, use, short, verb, done string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var w webhookView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/webhooks/"+args[0]+"/"+verb, nil, &w,
			); err != nil {
				return err
			}
			cmd.Printf("%s %s\n", w.Name, done)
			return render(cmd.OutOrStdout(), g.output, w, table{
				headers: webhookHeaders,
				rows:    [][]string{webhookRow(w)},
			})
		},
	}
}

func newWebhookPauseCmd(g *globals) *cobra.Command {
	return webhookAction(g, "pause <name|id>", "Stop sending to a webhook",
		"pause", "will receive nothing until it is resumed")
}

func newWebhookResumeCmd(g *globals) *cobra.Command {
	return webhookAction(g, "resume <name|id>", "Start sending to a webhook again",
		"resume", "will receive events again")
}

func newWebhookDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a webhook and anything queued for it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/webhooks/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
