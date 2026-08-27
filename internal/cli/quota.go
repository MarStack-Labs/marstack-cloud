package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type quotaLimits struct {
	Instances int `json:"instances"`
	VCPU      int `json:"vcpu"`
	MemoryMiB int `json:"memory_mib"`
	Volumes   int `json:"volumes"`
	VolumeGiB int `json:"volume_gib"`
}

type quotaView struct {
	ProjectID string      `json:"project_id"`
	Limits    quotaLimits `json:"limits"`
	Used      quotaLimits `json:"used"`
	UpdatedAt string      `json:"updated_at,omitempty"`
}

type quotaListView struct {
	Quotas []quotaView `json:"quotas"`
}

var quotaHeaders = []string{"PROJECT", "INSTANCES", "VCPU", "MEMORY MiB", "VOLUMES", "VOLUME GiB"}

func against(used, limit int) string {
	if limit == 0 {
		return strconv.Itoa(used) + " / ∞"
	}
	return strconv.Itoa(used) + " / " + strconv.Itoa(limit)
}

func quotaRow(q quotaView) []string {
	return []string{
		q.ProjectID,
		against(q.Used.Instances, q.Limits.Instances),
		against(q.Used.VCPU, q.Limits.VCPU),
		against(q.Used.MemoryMiB, q.Limits.MemoryMiB),
		against(q.Used.Volumes, q.Limits.Volumes),
		against(q.Used.VolumeGiB, q.Limits.VolumeGiB),
	}
}

func newQuotaCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Show and set what a project may consume",
		Long: "Show and set what a project may consume.\n\n" +
			"A limit of zero means no limit. Limits are checked when work is created, so\n" +
			"lowering a limit below what a project already holds does not delete anything;\n" +
			"it stops the project growing until it fits again.",
		Aliases: []string{"quotas"},
	}
	cmd.AddCommand(newQuotaShowCmd(g), newQuotaListCmd(g), newQuotaSetCmd(g), newQuotaClearCmd(g))
	return cmd
}

func newQuotaShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show [project]",
		Short: "Show the limits and usage of a project, defaulting to your own",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/quota"
			if len(args) == 1 {
				path = "/v1/quotas/" + args[0]
			}

			var q quotaView
			if err := g.client().do(cmd.Context(), "GET", path, nil, &q); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, q, table{
				headers: quotaHeaders,
				rows:    [][]string{quotaRow(q)},
			})
		},
	}
}

func newQuotaListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every project that carries a limit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list quotaListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/quotas", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Quotas))
			for _, q := range list.Quotas {
				rows = append(rows, quotaRow(q))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{
				headers: quotaHeaders,
				rows:    rows,
			})
		},
	}
}

func newQuotaSetCmd(g *globals) *cobra.Command {
	var req quotaLimits

	cmd := &cobra.Command{
		Use:   "set <project>",
		Short: "Replace the limits of a project",
		Long: "Replace the limits of a project.\n\n" +
			"Every limit is replaced, not merged: a flag you leave out becomes zero, which\n" +
			"means unlimited. Pass all the limits you want to keep.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var q quotaView
			err := g.client().do(cmd.Context(), "PUT", "/v1/quotas/"+args[0], req, &q)
			if err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, q, table{
				headers: quotaHeaders,
				rows:    [][]string{quotaRow(q)},
			})
		},
	}

	cmd.Flags().IntVar(&req.Instances, "instances", 0, "most instances, 0 for unlimited")
	cmd.Flags().IntVar(&req.VCPU, "vcpu", 0, "most vCPU in total, 0 for unlimited")
	cmd.Flags().IntVar(&req.MemoryMiB, "memory-mib", 0, "most memory in total, 0 for unlimited")
	cmd.Flags().IntVar(&req.Volumes, "volumes", 0, "most volumes, 0 for unlimited")
	cmd.Flags().IntVar(&req.VolumeGiB, "volume-gib", 0,
		"most volume capacity in total, 0 for unlimited")

	return cmd
}

func newQuotaClearCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <project>",
		Short: "Remove every limit from a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.client().do(cmd.Context(), "DELETE", "/v1/quotas/"+args[0], nil, nil)
		},
	}
}
