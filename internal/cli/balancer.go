package cli

import (
	"time"

	"fmt"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/certs"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/interval"
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
	Routes     []balancerRouteView   `json:"routes,omitempty"`
	Family     string                `json:"family,omitempty"`
	TLS        *balancerTLSView      `json:"tls,omitempty"`
	CreatedAt  string                `json:"created_at"`
}

type balancerRouteView struct {
	Host     string                `json:"host"`
	Path     string                `json:"path"`
	Service  string                `json:"service"`
	Backends []balancerBackendView `json:"backends"`
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

	name := b.TLS.Subject
	if name == "" {
		name = "terminated"
	}

	state, left, readable := certs.Life(b.TLS.ExpiresAt, time.Now())
	if !readable || state == certs.Fresh {
		return name
	}
	if state == certs.Expired {
		return name + " (expired)"
	}
	return name + " (" + interval.Human(left) + " left)"
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
	if len(b.Routes) > 0 {
		source = strconv.Itoa(len(b.Routes)) + " routes"
		up, total := 0, 0
		for _, route := range b.Routes {
			for _, backend := range route.Backends {
				total++
				if backend.Healthy {
					up++
				}
			}
		}

		return []string{
			b.Name,
			strconv.Itoa(b.ListenPort) + "/" + b.Protocol,
			strconv.Itoa(b.TargetPort),
			"by host and path",
			checkText(b),
			tlsText(b),
			source,
			strconv.Itoa(up) + "/" + strconv.Itoa(total) + " up",
		}
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
		rows = append(rows, []string{backend.InstanceID, address, state, probe,
			whyText(backend)})
	}
	return rows
}

func whyText(backend balancerBackendView) string {
	if backend.Reason != "" {
		return backend.Reason
	}
	if !backend.Running {
		return "the instance is not running"
	}
	return "-"
}

func backendRow(backend balancerBackendView) []string {
	state := "down"
	if backend.Healthy {
		state = "up"
	}
	address := backend.Address
	if address == "" {
		address = "-"
	}
	return []string{backend.InstanceID, address, state, whyText(backend)}
}

var routeHeaders = []string{"MATCH", "SERVICE", "INSTANCE", "ADDRESS", "STATE", "WHY"}

func matchText(route balancerRouteView) string {
	host := route.Host
	if host == "" {
		host = "*"
	}
	path := route.Path
	if path == "" {
		path = "/"
	}
	return host + path
}

func routeRows(b balancerView) [][]string {
	rows := make([][]string, 0, len(b.Routes))
	for _, route := range b.Routes {
		match, service := matchText(route), route.Service
		if len(route.Backends) == 0 {
			rows = append(rows, []string{match, service, "-", "-", "down",
				"the service holds no replica"})
			continue
		}

		for _, backend := range route.Backends {
			row := append([]string{match, service}, backendRow(backend)...)
			rows = append(rows, row)
			match, service = "", ""
		}
	}
	return rows
}

func renderRoutes(cmd *cobra.Command, g *globals, b balancerView) error {
	return render(cmd.OutOrStdout(), g.output, b, table{
		headers: routeHeaders,
		rows:    routeRows(b),
	})
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
		newBalancerRouteCmd(g),
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
	cmd.AddCommand(newBalancerSetCertificateCmd(g), newBalancerClearCertificateCmd(g),
		newBalancerACMECmd(g))
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
		Name       string      `json:"name"`
		Protocol   string      `json:"protocol,omitempty"`
		ListenPort int         `json:"listen_port,omitempty"`
		TargetPort int         `json:"target_port"`
		Algorithm  string      `json:"algorithm,omitempty"`
		Service    string      `json:"service,omitempty"`
		Check      string      `json:"check,omitempty"`
		CheckPath  string      `json:"check_path,omitempty"`
		Rise       int         `json:"rise,omitempty"`
		Fall       int         `json:"fall,omitempty"`
		Family     string      `json:"family,omitempty"`
		Instances  []string    `json:"instances,omitempty"`
		Routes     []routeBody `json:"routes,omitempty"`
	}
	var routes []string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a balancer in front of a set of instances",
		Long: "Create a balancer in front of a set of instances.\n\n" +
			"Every node claims the listen port and rewrites arriving packets to one of the\n" +
			"backends with nftables. There is no single virtual address: any node's address is\n" +
			"an entry point, so losing a node costs only the clients that were using it.\n\n" +
			"With --route the balancer reads each request and picks a service by the Host\n" +
			"header and the path, so one port serves many applications: --route app.test=web\n" +
			"--route app.test/api=api. The most specific rule wins - an exact host before any\n" +
			"host, then the longest path - and a request that matches nothing gets a 404.\n" +
			"Routes cannot be combined with --service or --instance, since a request cannot be\n" +
			"answered two ways, and they need tcp because there is no request in a datagram.\n\n" +
			"With --service the backends are whatever replicas that service currently holds,\n" +
			"so scaling the service moves traffic and nothing has to be registered by hand.\n\n" +
			"Without --check a backend counts as up while its instance is observed running,\n" +
			"which a process that is running but wedged still satisfies. With --check the node\n" +
			"holding the instance probes it every reconcile pass, and only a backend that\n" +
			"answers takes traffic.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			parsed, err := parseRoutes(routes)
			if err != nil {
				return err
			}
			req.Routes = parsed

			var created balancerView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/balancers", req, &created,
			); err != nil {
				return err
			}
			if len(created.Routes) > 0 {
				return renderRoutes(cmd, g, created)
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
	cmd.Flags().StringArrayVar(&routes, "route", nil,
		"send one host and path prefix to one service, as host[/path]=service. "+
			"Repeatable, and a rule starting with / matches any host")
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
			if len(b.Routes) > 0 {
				return renderRoutes(cmd, g, b)
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

type routeBody struct {
	Host    string `json:"host,omitempty"`
	Path    string `json:"path,omitempty"`
	Service string `json:"service"`
}

func parseRoutes(asked []string) ([]routeBody, error) {
	if len(asked) == 0 {
		return nil, nil
	}

	routes := make([]routeBody, 0, len(asked))
	for _, one := range asked {
		match, service, found := strings.Cut(one, "=")
		if !found {
			return nil, fmt.Errorf(
				"route %q needs a service: write it as host[/path]=service", one)
		}
		if strings.TrimSpace(service) == "" {
			return nil, fmt.Errorf("route %q names no service", one)
		}
		if strings.TrimSpace(match) == "" {
			return nil, fmt.Errorf(
				"route %q matches nothing: write /=%s to answer every request", one, service)
		}

		host, path, split := strings.Cut(match, "/")
		if split {
			path = "/" + path
		}
		routes = append(routes, routeBody{
			Host:    strings.TrimSpace(host),
			Path:    path,
			Service: strings.TrimSpace(service),
		})
	}
	return routes, nil
}

func newBalancerRouteCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Change which host and path each service answers",
		Long: "Change which host and path each service answers.\n\n" +
			"Only a balancer that was created with routes can be changed this way. A balancer\n" +
			"that sends everything on its port to one set of backends stays that way, because\n" +
			"turning one into the other changes what the port answers with no way back.\n\n" +
			"The node picks the new table up on its next pass, and it does so without\n" +
			"restarting the listener, so live connections are not dropped.",
	}
	cmd.AddCommand(newBalancerRouteSetCmd(g))
	return cmd
}

func newBalancerRouteSetCmd(g *globals) *cobra.Command {
	var routes []string

	cmd := &cobra.Command{
		Use:   "set <name|id>",
		Short: "Replace every route on a balancer",
		Long: "Replace every route on a balancer.\n\n" +
			"This replaces the whole table rather than merging, so what you pass is what the\n" +
			"balancer has afterwards. Merging would leave no way to remove one route without\n" +
			"inventing a delete route, and would make the call non-idempotent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := parseRoutes(routes)
			if err != nil {
				return err
			}

			body := struct {
				Routes []routeBody `json:"routes"`
			}{Routes: parsed}

			var updated balancerView
			if err := g.client().do(
				cmd.Context(), "PUT", "/v1/balancers/"+args[0]+"/routes", body, &updated,
			); err != nil {
				return err
			}
			cmd.PrintErrln("the node picks the new routes up on its next pass")
			return renderRoutes(cmd, g, updated)
		},
	}

	cmd.Flags().StringArrayVar(&routes, "route", nil,
		"send one host and path prefix to one service, as host[/path]=service. "+
			"Repeatable, and a rule starting with / matches any host")
	must(cmd.MarkFlagRequired("route"))
	return cmd
}

type acmeView struct {
	BalancerID string   `json:"balancer_id"`
	Names      []string `json:"names"`
	State      string   `json:"state"`
	Message    string   `json:"message"`
	ExpiresAt  string   `json:"expires_at"`
	IssuedAt   string   `json:"issued_at"`
}

var acmeHeaders = []string{"BALANCER", "NAMES", "STATE", "EXPIRES", "WHY"}

func acmeRow(one acmeView) []string {
	return []string{
		one.BalancerID,
		strings.Join(one.Names, ", "),
		one.State,
		one.ExpiresAt[:min(len(one.ExpiresAt), 10)],
		shortenLine(one.Message, 44),
	}
}

func newBalancerACMECmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auto",
		Short: "Let a certificate authority issue and renew the certificate",
		Long: "Let a certificate authority issue and renew the certificate.\n\n" +
			"The authority proves the name belongs to you by fetching\n" +
			"http://<name>/.well-known/acme-challenge/<token>, which the node answers on the\n" +
			"balancer's own listen port. That only works if the authority can reach it, so a\n" +
			"public authority needs the balancer listening on port 80 and the name pointing\n" +
			"at a node.\n\n" +
			"Renewal happens on its own thirty days before expiry. Nothing is uploaded and\n" +
			"no private key ever leaves the platform.",
	}
	cmd.AddCommand(newACMEStartCmd(g), newACMEStatusCmd(g), newACMEStopCmd(g),
		newACMEListCmd(g))
	return cmd
}

func newACMEStartCmd(g *globals) *cobra.Command {
	var names []string

	cmd := &cobra.Command{
		Use:   "start <balancer>",
		Short: "Ask for a certificate and keep it renewed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := struct {
				Names []string `json:"names,omitempty"`
			}{Names: names}

			var started acmeView
			if err := g.client().do(cmd.Context(), "POST",
				"/v1/balancers/"+args[0]+"/certificate/acme", body, &started); err != nil {
				return err
			}

			cmd.PrintErrln("the authority is asked on the next pass; watch it with " +
				"marstack balancer certificate auto status " + args[0])
			return render(cmd.OutOrStdout(), g.output, started,
				table{headers: acmeHeaders, rows: [][]string{acmeRow(started)}})
		},
	}

	cmd.Flags().StringSliceVar(&names, "name", nil,
		"host to cover, repeatable; defaults to every host the balancer routes")
	return cmd
}

func newACMEStatusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status <balancer>",
		Short: "Show where the certificate order got to",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var held acmeView
			if err := g.client().do(cmd.Context(), "GET",
				"/v1/balancers/"+args[0]+"/certificate/acme", nil, &held); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, held,
				table{headers: acmeHeaders, rows: [][]string{acmeRow(held)}})
		},
	}
}

func newACMEStopCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <balancer>",
		Short: "Stop renewing; the certificate already attached stays",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(cmd.Context(), "DELETE",
				"/v1/balancers/"+args[0]+"/certificate/acme", nil, nil); err != nil {
				return err
			}
			cmd.Printf("stopped renewing %s\n", args[0])
			return nil
		},
	}
}

func newACMEListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every certificate this platform keeps renewed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list struct {
				Certificates []acmeView `json:"certificates"`
			}
			if err := g.client().do(cmd.Context(), "GET", "/v1/certificates",
				nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Certificates))
			for _, one := range list.Certificates {
				rows = append(rows, acmeRow(one))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: acmeHeaders, rows: rows})
		},
	}
}
