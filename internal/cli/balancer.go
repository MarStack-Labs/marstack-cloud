package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type balancerBackendView struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address"`
	Healthy    bool   `json:"healthy"`
	Running    bool   `json:"running"`
	Probe      string `json:"probe"`
	Reason     string `json:"reason"`
	CheckedAt  string `json:"checked_at"`
	AddedAt    string `json:"added_at"`
}

type balancerView struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Protocol   string                `json:"protocol"`
	ListenPort int                   `json:"listen_port"`
	TargetPort int                   `json:"target_port"`
	Algorithm  string                `json:"algorithm"`
	Service    string                `json:"service"`
	Check      string                `json:"check"`
	CheckPath  string                `json:"check_path"`
	Rise       int                   `json:"rise"`
	Fall       int                   `json:"fall"`
	Backends   []balancerBackendView `json:"backends"`
	Family     string                `json:"family,omitempty"`
	TLS        *balancerTLSView      `json:"tls,omitempty"`
	CreatedAt  string                `json:"created_at"`
}

type balancerTLSView struct {
	Subject   string `json:"subject"`
	ExpiresAt string `json:"expires_at"`
}

type balancerListView struct {
	Balancers []balancerView `json:"balancers"`
}

var balancerHeaders = []string{
	"NAME", "LISTEN", "TARGET", "ALGORITHM", "CHECK", "TLS", "SOURCE", "BACKENDS",
}

func tlsText(b balancerView) string {
	if b.TLS == nil {
		return "-"
	}
	if b.TLS.Subject == "" {
		return "terminated"
	}
	return b.TLS.Subject
}

func balancerRow(b balancerView) []string {
	up := 0
	for _, backend := range b.Backends {
		if backend.Healthy {
			up++
		}
	}

	source := "instances"
	if b.Service != "" {
		source = "service " + b.Service
	}

	return []string{
		b.Name,
		strconv.Itoa(b.ListenPort) + "/" + b.Protocol,
		strconv.Itoa(b.TargetPort),
		b.Algorithm,
		checkText(b),
		tlsText(b),
		source,
		strconv.Itoa(up) + "/" + strconv.Itoa(len(b.Backends)) + " up",
	}
}

func checkText(b balancerView) string {
	switch b.Check {
	case "http":
		return "http " + b.CheckPath + " " + thresholdText(b)
	case "tcp":
		return "tcp " + thresholdText(b)
	default:
		return "vm liveness"
	}
}

func thresholdText(b balancerView) string {
	return "(" + strconv.Itoa(b.Rise) + "/" + strconv.Itoa(b.Fall) + ")"
}

var backendHeaders = []string{"INSTANCE", "ADDRESS", "STATE", "PROBE", "WHY"}

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

		probe := backend.Probe
		if probe == "" {
			probe = "-"
		}
		why := backend.Reason
		if why == "" && !backend.Running {
			why = "the instance is not running"
		}
		if why == "" {
			why = "-"
		}
		rows = append(rows, []string{backend.InstanceID, address, state, probe, why})
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
		newBalancerCertificateCmd(g),
	)
	return cmd
}

func renderBalancer(cmd *cobra.Command, g *globals, b balancerView) error {
	return render(cmd.OutOrStdout(), g.output, b, table{
		headers: balancerHeaders,
		rows:    [][]string{balancerRow(b)},
	})
}

func newBalancerCertificateCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "certificate",
		Short: "Terminate TLS on a balancer's listen port",
		Long: "Terminate TLS on a balancer's listen port.\n\n" +
			"A balancer without a certificate is an nftables rule: the kernel rewrites the\n" +
			"destination and never looks at the bytes. A balancer with one cannot be, because\n" +
			"nothing in nftables terminates TLS, so the node accepts the connection itself and\n" +
			"opens a plain one to the backend.\n\n" +
			"The private key is sealed with the operator key and served only to nodes.",
	}
	cmd.AddCommand(newBalancerSetCertificateCmd(g), newBalancerClearCertificateCmd(g))
	return cmd
}

func newBalancerSetCertificateCmd(g *globals) *cobra.Command {
	var certFile, keyFile string

	cmd := &cobra.Command{
		Use:   "set <balancer id>",
		Short: "Give a balancer a certificate to terminate with",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			certPEM, err := os.ReadFile(certFile)
			if err != nil {
				return fmt.Errorf("read %s: %w", certFile, err)
			}
			keyPEM, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("read %s: %w", keyFile, err)
			}

			body := struct {
				Certificate string `json:"certificate"`
				PrivateKey  string `json:"private_key"`
			}{Certificate: string(certPEM), PrivateKey: string(keyPEM)}

			var updated balancerView
			if err := g.client().do(
				cmd.Context(), "PUT", "/v1/balancers/"+args[0]+"/certificate", body, &updated,
			); err != nil {
				return err
			}
			cmd.PrintErrln("the node picks the certificate up on its next pass")
			return renderBalancer(cmd, g, updated)
		},
	}

	cmd.Flags().StringVar(&certFile, "cert-file", "", "PEM certificate chain, leaf first")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "PEM private key for the leaf certificate")
	must(cmd.MarkFlagRequired("cert-file"))
	must(cmd.MarkFlagRequired("key-file"))
	return cmd
}

func newBalancerClearCertificateCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <balancer id>",
		Short: "Stop terminating TLS and go back to an nftables rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var updated balancerView
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/balancers/"+args[0]+"/certificate", nil, &updated,
			); err != nil {
				return err
			}
			return renderBalancer(cmd, g, updated)
		},
	}
}

func newBalancerCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name       string   `json:"name"`
		Protocol   string   `json:"protocol,omitempty"`
		ListenPort int      `json:"listen_port,omitempty"`
		TargetPort int      `json:"target_port"`
		Algorithm  string   `json:"algorithm,omitempty"`
		Service    string   `json:"service,omitempty"`
		Check      string   `json:"check,omitempty"`
		CheckPath  string   `json:"check_path,omitempty"`
		Rise       int      `json:"rise,omitempty"`
		Fall       int      `json:"fall,omitempty"`
		Family     string   `json:"family,omitempty"`
		Instances  []string `json:"instances,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a balancer in front of a set of instances",
		Long: "Create a balancer in front of a set of instances.\n\n" +
			"Every node claims the listen port and rewrites arriving packets to one of the\n" +
			"backends with nftables. There is no single virtual address: any node's address is\n" +
			"an entry point, so losing a node costs only the clients that were using it.\n\n" +
			"With --service the backends are whatever replicas that service currently holds,\n" +
			"so scaling the service moves traffic and nothing has to be registered by hand.\n\n" +
			"Without --check a backend counts as up while its instance is observed running,\n" +
			"which a process that is running but wedged still satisfies. With --check the node\n" +
			"holding the instance probes it every reconcile pass, and only a backend that\n" +
			"answers takes traffic.",
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
	cmd.Flags().StringVar(&req.Family, "family", "",
		"which of a dual stack instance's addresses to balance: ipv4 or ipv6")
	cmd.Flags().StringVar(&req.Algorithm, "algorithm", "round_robin",
		"round_robin or source_hash, which keeps one client on one backend")
	cmd.Flags().StringVar(&req.Service, "service", "",
		"follow a service: backends join and leave with its replica count")
	cmd.Flags().StringVar(&req.Check, "check", "none",
		"none, tcp, or http: what makes a backend count as up")
	cmd.Flags().StringVar(&req.CheckPath, "check-path", "",
		"path an http check asks for, defaults to /")
	cmd.Flags().IntVar(&req.Rise, "rise", 0,
		"consecutive passes before a backend takes traffic, defaults to 2")
	cmd.Flags().IntVar(&req.Fall, "fall", 0,
		"consecutive failures before a backend stops taking traffic, defaults to 2")
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
