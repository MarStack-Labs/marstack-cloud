package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

type forwardView struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	Protocol   string `json:"protocol"`
	NodePort   int    `json:"node_port"`
	TargetPort int    `json:"target_port"`
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	TLS        *struct {
		Subject   string `json:"subject"`
		ExpiresAt string `json:"expires_at"`
	} `json:"tls,omitempty"`
	CreatedAt string `json:"created_at"`
}

type forwardListView struct {
	Forwards []forwardView `json:"forwards"`
}

var forwardHeaders = []string{"ID", "NODE PORT", "TARGET", "INSTANCE", "NODE", "TLS"}

func forwardRow(f forwardView) []string {
	return []string{
		f.ID,
		strconv.Itoa(f.NodePort) + "/" + f.Protocol,
		f.Address + ":" + strconv.Itoa(f.TargetPort),
		f.InstanceID,
		f.NodeID,
		forwardTLS(f),
	}
}

func forwardTLS(f forwardView) string {
	if f.TLS == nil {
		return "-"
	}
	if f.TLS.Subject == "" {
		return "terminated"
	}
	return f.TLS.Subject
}

func renderForward(cmd *cobra.Command, g *globals, f forwardView) error {
	return render(cmd.OutOrStdout(), g.output, f, table{
		headers: forwardHeaders,
		rows:    [][]string{forwardRow(f)},
	})
}

func newForwardCertificateCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "certificate",
		Short: "Terminate TLS on a published port",
		Long: "Terminate TLS on a published port.\n\n" +
			"A published port without a certificate is an nftables rule and the kernel never\n" +
			"looks at the bytes. With one it cannot be, because nothing in nftables terminates\n" +
			"TLS, so the node accepts the connection and opens a plain one to the instance.\n\n" +
			"The private key is sealed with the operator key and served only to nodes.",
	}
	cmd.AddCommand(newForwardSetCertificateCmd(g), newForwardClearCertificateCmd(g))
	return cmd
}

func newForwardSetCertificateCmd(g *globals) *cobra.Command {
	var certFile, keyFile string

	cmd := &cobra.Command{
		Use:   "set <forward id>",
		Short: "Give a published port a certificate to terminate with",
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

			var updated forwardView
			if err := g.client().do(
				cmd.Context(), "PUT", "/v1/forwards/"+args[0]+"/certificate", body, &updated,
			); err != nil {
				return err
			}
			cmd.PrintErrln("the node picks the certificate up on its next pass")
			return renderForward(cmd, g, updated)
		},
	}

	cmd.Flags().StringVar(&certFile, "cert-file", "", "PEM certificate chain, leaf first")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "PEM private key for the leaf certificate")
	must(cmd.MarkFlagRequired("cert-file"))
	must(cmd.MarkFlagRequired("key-file"))
	return cmd
}

func newForwardClearCertificateCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <forward id>",
		Short: "Stop terminating TLS and go back to an nftables rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var updated forwardView
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/forwards/"+args[0]+"/certificate", nil, &updated,
			); err != nil {
				return err
			}
			return renderForward(cmd, g, updated)
		},
	}
}

func newForwardCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "forward",
		Short:   "Publish an instance port on the node that runs it",
		Aliases: []string{"forwards"},
	}
	cmd.AddCommand(newForwardCreateCmd(g), newForwardListCmd(g), newForwardDeleteCmd(g),
		newForwardCertificateCmd(g))
	return cmd
}

func newForwardCreateCmd(g *globals) *cobra.Command {
	var req struct {
		InstanceID string `json:"instance_id"`
		Protocol   string `json:"protocol,omitempty"`
		NodePort   int    `json:"node_port,omitempty"`
		TargetPort int    `json:"target_port"`
		Family     string `json:"family,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Publish a port, reachable on the address the node reported",
		Long: "Publish a port, reachable on the address the node reported.\n\n" +
			"The node rewrites arriving packets to the instance with nftables. Nothing binds a\n" +
			"socket, so a privileged node port is fine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created forwardView
			if err := g.client().do(cmd.Context(), "POST", "/v1/forwards", req, &created); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: forwardHeaders,
				rows:    [][]string{forwardRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.InstanceID, "instance", "", "instance id whose port is published")
	cmd.Flags().IntVar(&req.TargetPort, "target-port", 0, "port inside the instance")
	cmd.Flags().IntVar(&req.NodePort, "node-port", 0, "port on the node, defaults to the target port")
	cmd.Flags().StringVar(&req.Family, "family", "",
		"which of a dual stack instance's addresses to publish: ipv4 or ipv6")
	cmd.Flags().StringVar(&req.Protocol, "protocol", "tcp", "tcp or udp")
	must(cmd.MarkFlagRequired("instance"))
	must(cmd.MarkFlagRequired("target-port"))

	return cmd
}

func newForwardListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List published ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list forwardListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/forwards", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Forwards))
			for _, f := range list.Forwards {
				rows = append(rows, forwardRow(f))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: forwardHeaders, rows: rows})
		},
	}
}

func newForwardDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Stop publishing a port",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/forwards/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
