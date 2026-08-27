package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type balancerBackendView struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address"`
	Healthy    bool   `json:"healthy"`
	AddedAt    string `json:"added_at"`
}

type balancerView struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Protocol   string                `json:"protocol"`
	ListenPort int                   `json:"listen_port"`
	TargetPort int                   `json:"target_port"`
	Algorithm  string                `json:"algorithm"`
	Backends   []balancerBackendView `json:"backends"`
	CreatedAt  string                `json:"created_at"`
}

type balancerListView struct {
	Balancers []balancerView `json:"balancers"`
}

var balancerHeaders = []string{"NAME", "ID", "LISTEN", "TARGET", "ALGORITHM", "BACKENDS"}

func balancerRow(b balancerView) []string {
	up := 0
	for _, backend := range b.Backends {
		if backend.Healthy {
			up++
		}
	}

	return []string{
		b.Name,
		b.ID,
		strconv.Itoa(b.ListenPort) + "/" + b.Protocol,
		strconv.Itoa(b.TargetPort),
		b.Algorithm,
		strconv.Itoa(up) + "/" + strconv.Itoa(len(b.Backends)) + " up",
	}
}

var backendHeaders = []string{"INSTANCE", "ADDRESS", "STATE"}

func backendRows(b balancerView) [][]string {
	rows := make([][]string, 0, len(b.Backends))
	for _, backend := range b.Backends {
		state := "down"
		if backend.Healthy {
			state = "up"
		}
		address := backend.Address
		if address == "" {
			address = "-"
		}
		rows = append(rows, []string{backend.InstanceID, address, state})
	}
	return rows
}

func newBalancerCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "balancer",
		Short:   "Spread one port across several instances",
		Aliases: []string{"balancers", "lb"},
	}
	cmd.AddCommand(
		newBalancerCreateCmd(g),
		newBalancerListCmd(g),
		newBalancerGetCmd(g),
		newBalancerAddCmd(g),
		newBalancerRemoveCmd(g),
		newBalancerDeleteCmd(g),
	)
	return cmd
}

func newBalancerCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name       string   `json:"name"`
		Protocol   string   `json:"protocol,omitempty"`
		ListenPort int      `json:"listen_port,omitempty"`
		TargetPort int      `json:"target_port"`
		Algorithm  string   `json:"algorithm,omitempty"`
		Instances  []string `json:"instances,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a balancer in front of a set of instances",
		Long: "Create a balancer in front of a set of instances.\n\n" +
			"Every node claims the listen port and rewrites arriving packets to one of the\n" +
			"backends with nftables. There is no single virtual address: any node's address is\n" +
			"an entry point, so losing a node costs only the clients that were using it.\n\n" +
			"A backend only receives traffic while its instance is observed running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created balancerView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/balancers", req, &created,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: balancerHeaders,
				rows:    [][]string{balancerRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "balancer name, unique in the project")
	cmd.Flags().IntVar(&req.TargetPort, "target-port", 0, "port inside every backend")
	cmd.Flags().IntVar(&req.ListenPort, "listen-port", 0,
		"port claimed on every node, defaults to the target port")
	cmd.Flags().StringVar(&req.Protocol, "protocol", "tcp", "tcp or udp")
	cmd.Flags().StringVar(&req.Algorithm, "algorithm", "round_robin",
		"round_robin or source_hash, which keeps one client on one backend")
	cmd.Flags().StringSliceVar(&req.Instances, "instance", nil,
		"backend instance id, repeatable")
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("target-port"))

	return cmd
}

func newBalancerListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List balancers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list balancerListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/balancers", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Balancers))
			for _, b := range list.Balancers {
				rows = append(rows, balancerRow(b))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: balancerHeaders, rows: rows})
		},
	}
}

func newBalancerGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|id>",
		Short: "Show a balancer and the state of every backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var b balancerView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/balancers/"+args[0], nil, &b,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, b, table{
				headers: backendHeaders,
				rows:    backendRows(b),
			})
		},
	}
}

func newBalancerAddCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "add <name|id> <instance-id>",
		Short: "Add a backend to a balancer",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := struct {
				InstanceID string `json:"instance_id"`
			}{InstanceID: args[1]}

			var b balancerView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/balancers/"+args[0]+"/backends", body, &b,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, b, table{
				headers: backendHeaders,
				rows:    backendRows(b),
			})
		},
	}
}

func newBalancerRemoveCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name|id> <instance-id>",
		Short: "Take a backend out of a balancer",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/balancers/" + args[0] + "/backends/" + args[1]
			if err := g.client().do(cmd.Context(), "DELETE", path, nil, nil); err != nil {
				return err
			}
			cmd.Printf("removed %s from %s\n", args[1], args[0])
			return nil
		},
	}
}

func newBalancerDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a balancer and release its listen port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/balancers/"+strings.TrimSpace(args[0]), nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
