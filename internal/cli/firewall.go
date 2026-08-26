package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func errUsage(message string) error {
	return errors.New(message)
}

type firewallRuleView struct {
	Protocol string `json:"protocol,omitempty"`
	FromPort int    `json:"from_port,omitempty"`
	ToPort   int    `json:"to_port,omitempty"`
	Source   string `json:"source,omitempty"`
}

type firewallView struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Rules     []firewallRuleView `json:"rules"`
	CreatedAt string             `json:"created_at"`
}

type firewallListView struct {
	Firewalls []firewallView `json:"firewalls"`
}

var firewallHeaders = []string{"NAME", "ID", "RULES"}

func firewallRow(f firewallView) []string {
	if len(f.Rules) == 0 {
		return []string{f.Name, f.ID, "none (nothing may reach it)"}
	}

	parts := make([]string, 0, len(f.Rules))
	for _, rule := range f.Rules {
		port := ""
		switch {
		case rule.FromPort == 0:
		case rule.FromPort == rule.ToPort:
			port = ":" + strconv.Itoa(rule.FromPort)
		default:
			port = ":" + strconv.Itoa(rule.FromPort) + "-" + strconv.Itoa(rule.ToPort)
		}
		parts = append(parts, rule.Protocol+port+" from "+rule.Source)
	}
	return []string{f.Name, f.ID, strings.Join(parts, ", ")}
}

func newFirewallCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "firewall",
		Short:   "Manage the rules that decide what may reach an instance",
		Aliases: []string{"firewalls"},
	}
	cmd.AddCommand(newFirewallCreateCmd(g), newFirewallListCmd(g),
		newFirewallRulesCmd(g), newFirewallDeleteCmd(g))
	return cmd
}

func parseRules(specs []string) ([]firewallRuleView, error) {
	rules := make([]firewallRuleView, 0, len(specs))
	for _, spec := range specs {
		fields := strings.Split(spec, ",")
		rule := firewallRuleView{Protocol: "tcp", Source: "0.0.0.0/0"}

		for _, field := range fields {
			key, value, found := strings.Cut(strings.TrimSpace(field), "=")
			if !found {
				return nil, errUsage("each rule field looks like key=value, got " + field)
			}

			switch key {
			case "protocol":
				rule.Protocol = value
			case "source":
				rule.Source = value
			case "port":
				from, to, ranged := strings.Cut(value, "-")
				number, err := strconv.Atoi(from)
				if err != nil {
					return nil, errUsage("port must be a number, got " + from)
				}
				rule.FromPort = number

				if ranged {
					upper, err := strconv.Atoi(to)
					if err != nil {
						return nil, errUsage("port range must be numbers, got " + to)
					}
					rule.ToPort = upper
				}
			default:
				return nil, errUsage("unknown rule field " + key)
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func newFirewallCreateCmd(g *globals) *cobra.Command {
	var name string
	var specs []string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a firewall, allowing only what its rules name",
		Long: "Create a firewall, allowing only what its rules name.\n\n" +
			"A rule is a comma separated list: --rule 'protocol=tcp,port=80'\n" +
			"                                  --rule 'port=8000-8100,source=10.20.0.0/16'\n" +
			"                                  --rule 'protocol=icmp'\n\n" +
			"An instance with no firewall is reachable from anywhere, as before. An instance\n" +
			"that names a firewall with no rules is reachable from nowhere.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rules, err := parseRules(specs)
			if err != nil {
				return err
			}

			body := struct {
				Name  string             `json:"name"`
				Rules []firewallRuleView `json:"rules,omitempty"`
			}{Name: name, Rules: rules}

			var created firewallView
			if err := g.client().do(cmd.Context(), "POST", "/v1/firewalls", body, &created); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: firewallHeaders,
				rows:    [][]string{firewallRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "firewall name, unique within the platform")
	cmd.Flags().StringArrayVar(&specs, "rule", nil, "an allow rule, repeatable")
	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newFirewallListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List firewalls and their rules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list firewallListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/firewalls", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Firewalls))
			for _, f := range list.Firewalls {
				rows = append(rows, firewallRow(f))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: firewallHeaders, rows: rows})
		},
	}
}

func newFirewallRulesCmd(g *globals) *cobra.Command {
	var specs []string

	cmd := &cobra.Command{
		Use:   "rules <name|id>",
		Short: "Replace the rules of a firewall",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rules, err := parseRules(specs)
			if err != nil {
				return err
			}

			body := struct {
				Rules []firewallRuleView `json:"rules"`
			}{Rules: rules}

			var updated firewallView
			if err := g.client().do(
				cmd.Context(), "PUT", "/v1/firewalls/"+args[0]+"/rules", body, &updated,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, updated, table{
				headers: firewallHeaders,
				rows:    [][]string{firewallRow(updated)},
			})
		},
	}

	cmd.Flags().StringArrayVar(&specs, "rule", nil, "an allow rule, repeatable; none means deny all")
	return cmd
}

func newFirewallDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a firewall",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/firewalls/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
