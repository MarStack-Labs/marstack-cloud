package cli

import (
	"github.com/spf13/cobra"
)

type projectView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type projectListView struct {
	Projects []projectView `json:"projects"`
}

var projectHeaders = []string{"NAME", "ID", "CREATED"}

func projectRow(p projectView) []string {
	created := p.CreatedAt
	if len(created) > 19 {
		created = created[:19]
	}
	return []string{p.Name, p.ID, created}
}

func newProjectCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
		Long: "Manage projects.\n\n" +
			"A project owns instances, networks, volumes, images, firewalls and published\n" +
			"ports. Every token belongs to one project and sees only what that project owns.\n" +
			"To work in another project, mint a token there.",
		Aliases: []string{"projects"},
	}
	cmd.AddCommand(newProjectCreateCmd(g), newProjectListCmd(g), newProjectDeleteCmd(g))
	return cmd
}

func newProjectCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name string `json:"name"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created projectView
			if err := g.client().do(cmd.Context(), "POST", "/v1/projects", req, &created); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: projectHeaders,
				rows:    [][]string{projectRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "project name, unique within the platform")
	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newProjectListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list projectListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/projects", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Projects))
			for _, p := range list.Projects {
				rows = append(rows, projectRow(p))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{
				headers: projectHeaders,
				rows:    rows,
			})
		},
	}
}

func newProjectDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an empty project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.client().do(cmd.Context(), "DELETE", "/v1/projects/"+args[0], nil, nil)
		},
	}
}
