package cli

import "github.com/spf13/cobra"

type networkView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"`
	Gateway   string `json:"gateway"`
	Bridge    string `json:"bridge"`
	CreatedAt string `json:"created_at"`
}

type networkListView struct {
	Networks []networkView `json:"networks"`
}

var networkHeaders = []string{"NAME", "ID", "CIDR", "GATEWAY", "BRIDGE"}

func networkRow(n networkView) []string {
	return []string{n.Name, n.ID, n.CIDR, n.Gateway, n.Bridge}
}

func newNetworkCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "network",
		Short:   "Manage networks",
		Aliases: []string{"networks"},
	}
	cmd.AddCommand(newNetworkCreateCmd(g), newNetworkListCmd(g), newNetworkDeleteCmd(g))
	return cmd
}

func newNetworkCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name string `json:"name"`
		CIDR string `json:"cidr,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a network",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created networkView
			if err := newClient(g.endpoint).do(cmd.Context(), "POST", "/v1/networks", req, &created); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: networkHeaders,
				rows:    [][]string{networkRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "network name, unique within the platform")
	cmd.Flags().StringVar(&req.CIDR, "cidr", "", "address range, must not overlap another network")
	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newNetworkListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List networks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list networkListView
			if err := newClient(g.endpoint).do(cmd.Context(), "GET", "/v1/networks", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Networks))
			for _, n := range list.Networks {
				rows = append(rows, networkRow(n))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: networkHeaders, rows: rows})
		},
	}
}

func newNetworkDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a network that has no addresses handed out",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := newClient(g.endpoint).do(
				cmd.Context(), "DELETE", "/v1/networks/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
