package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type serviceMemberView struct {
	InstanceID string `json:"instance_id"`
	Revision   int    `json:"revision"`
	CreatedAt  string `json:"created_at"`
}

type serviceRolloutView struct {
	Current int `json:"current"`
	Stale   int `json:"stale"`
}

type serviceView struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Replicas  int                 `json:"replicas"`
	Revision  int                 `json:"revision"`
	Isolation string              `json:"isolation"`
	Image     string              `json:"image"`
	Group     string              `json:"placement_group"`
	Blocked   string              `json:"blocked"`
	EnvNames  []string            `json:"env_names"`
	FilePaths []string            `json:"file_paths"`
	Rollout   serviceRolloutView  `json:"rollout"`
	Members   []serviceMemberView `json:"members"`
}

type serviceListView struct {
	Services []serviceView `json:"services"`
}

var serviceHeaders = []string{"NAME", "ID", "REPLICAS", "REV", "ISOLATION", "IMAGE", "STATE"}

func serviceRow(s serviceView) []string {
	state := strconv.Itoa(len(s.Members)) + "/" + strconv.Itoa(s.Replicas) + " up"
	if s.Rollout.Stale > 0 {
		state = "rolling out, " + strconv.Itoa(s.Rollout.Current) + "/" +
			strconv.Itoa(s.Replicas) + " on rev " + strconv.Itoa(s.Revision)
	}
	if s.Blocked != "" {
		state = "stuck: " + shortenLine(s.Blocked, 60)
	}

	return []string{s.Name, s.ID, strconv.Itoa(s.Replicas), strconv.Itoa(s.Revision),
		s.Isolation, s.Image, state}
}

func shortenLine(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit-1] + "…"
}

func newServiceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "service",
		Short:   "Keep a number of replicas of one workload running",
		Aliases: []string{"services", "svc"},
	}
	cmd.AddCommand(
		newServiceCreateCmd(g),
		newServiceListCmd(g),
		newServiceGetCmd(g),
		newServiceScaleCmd(g),
		newServiceUpdateCmd(g),
		newServiceDeleteCmd(g),
	)
	return cmd
}

func newServiceCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name          string            `json:"name"`
		Replicas      int               `json:"replicas"`
		Isolation     string            `json:"isolation,omitempty"`
		Image         string            `json:"image,omitempty"`
		ISO           string            `json:"iso,omitempty"`
		Kernel        string            `json:"kernel,omitempty"`
		DiskGiB       int               `json:"disk_gib,omitempty"`
		FirewallID    string            `json:"firewall_id,omitempty"`
		Command       []string          `json:"command,omitempty"`
		NetworkID     string            `json:"network_id,omitempty"`
		RestartPolicy string            `json:"restart_policy,omitempty"`
		VCPU          int               `json:"vcpu,omitempty"`
		MemoryMiB     int               `json:"memory_mib,omitempty"`
		Group         string            `json:"placement_group,omitempty"`
		Strict        bool              `json:"placement_strict,omitempty"`
		NodeSelector  map[string]string `json:"node_selector,omitempty"`
		ExtraNetworks []string          `json:"extra_networks,omitempty"`
		Env           map[string]string `json:"env,omitempty"`
		Files         []fileSpec        `json:"files,omitempty"`
		Keys          []string          `json:"keys,omitempty"`
	}
	var (
		selectors []string
		envPairs  []string
		networks  []string
		filePairs []string
	)

	cmd := &cobra.Command{
		Use:   "create [-- command args...]",
		Short: "Create a service and let the platform hold the replica count",
		Long: "Create a service and let the platform hold the replica count.\n\n" +
			"A service is a workload template plus a number. A loop makes replicas until the\n" +
			"count is met and replaces any that disappear, so a deleted or unrecoverable\n" +
			"replica comes back without anybody typing anything.\n\n" +
			"Replica names are the service name with a random suffix. Ordinals would collide\n" +
			"with an instance somebody made by hand and wedge the service permanently.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Command = args

			if len(networks) > 0 {
				req.NetworkID = networks[0]
				req.ExtraNetworks = networks[1:]
			}

			for _, pair := range envPairs {
				name, value, found := strings.Cut(pair, "=")
				if !found {
					return errors.New("an environment variable is NAME=value, and " +
						pair + " has no value")
				}
				if req.Env == nil {
					req.Env = map[string]string{}
				}
				req.Env[name] = value
			}

			files, err := readFiles(filePairs)
			if err != nil {
				return err
			}
			req.Files = files

			for _, pair := range selectors {
				key, value, found := strings.Cut(pair, "=")
				if !found {
					return errors.New(
						"a node selector is key=value, and " + pair + " has no value")
				}
				if req.NodeSelector == nil {
					req.NodeSelector = map[string]string{}
				}
				req.NodeSelector[key] = value
			}

			var created serviceView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/services", req, &created,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: serviceHeaders,
				rows:    [][]string{serviceRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "service name, unique in the project")
	cmd.Flags().IntVar(&req.Replicas, "replicas", 1, "how many replicas to hold")
	cmd.Flags().StringVar(&req.Isolation, "isolation", "container",
		"container, vm, microvm, or sandbox")
	cmd.Flags().StringVar(&req.Image, "image", "", "image every replica boots")
	cmd.Flags().StringVar(&req.ISO, "iso", "", "iso every replica boots")
	cmd.Flags().StringVar(&req.Kernel, "kernel", "", "kernel image for microvm and sandbox")
	cmd.Flags().IntVar(&req.DiskGiB, "disk-gib", 0, "root disk size for a vm")
	cmd.Flags().StringVar(&req.FirewallID, "firewall", "", "firewall every replica carries")
	cmd.Flags().StringVar(&req.RestartPolicy, "restart", "", "never, on-failure, or always")
	cmd.Flags().IntVar(&req.VCPU, "vcpu", 0, "virtual CPUs per replica")
	cmd.Flags().IntVar(&req.MemoryMiB, "memory-mib", 0, "memory per replica in MiB")
	cmd.Flags().StringArrayVar(&filePairs, "file", nil,
		"/path/in/the/workload=local-file[:mode] every replica gets, repeatable; sealed at rest")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil,
		"NAME=value every replica runs with, repeatable; sealed at rest and never served back")
	cmd.Flags().StringArrayVar(&networks, "network", nil,
		"network every replica joins, repeatable; the first is eth0")
	cmd.Flags().StringArrayVar(&selectors, "node-selector", nil,
		"only place replicas on nodes carrying key=value, repeatable and combined with and")
	cmd.Flags().StringVar(&req.Group, "placement-group", "",
		"spread replicas across zones and nodes")
	cmd.Flags().BoolVar(&req.Strict, "placement-strict", false,
		"refuse to place a replica that would break the spread")
	cmd.Flags().StringArrayVar(&req.Keys, "key", nil, "ssh key name, repeatable")
	must(cmd.MarkFlagRequired("name"))

	return cmd
}

func newServiceListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list serviceListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/services", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Services))
			for _, s := range list.Services {
				rows = append(rows, serviceRow(s))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: serviceHeaders, rows: rows})
		},
	}
}

var serviceMemberHeaders = []string{"REPLICA", "REV", "SINCE"}

func newServiceGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|id>",
		Short: "Show a service and the replicas it holds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var s serviceView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/services/"+args[0], nil, &s,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(s.Members))
			for _, member := range s.Members {
				rows = append(rows, []string{member.InstanceID,
					strconv.Itoa(member.Revision),
					member.CreatedAt[:min(len(member.CreatedAt), 19)]})
			}
			return render(cmd.OutOrStdout(), g.output, s,
				table{headers: serviceMemberHeaders, rows: rows})
		},
	}
}

func newServiceScaleCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "scale <name|id> <replicas>",
		Short: "Change how many replicas a service holds",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			replicas, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}

			body := struct {
				Replicas int `json:"replicas"`
			}{Replicas: replicas}

			var scaled serviceView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/services/"+args[0]+"/scale", body, &scaled,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, scaled, table{
				headers: serviceHeaders,
				rows:    [][]string{serviceRow(scaled)},
			})
		},
	}
}

func newServiceUpdateCmd(g *globals) *cobra.Command {
	var req struct {
		Isolation     string            `json:"isolation,omitempty"`
		Image         string            `json:"image,omitempty"`
		ISO           string            `json:"iso,omitempty"`
		Kernel        string            `json:"kernel,omitempty"`
		DiskGiB       int               `json:"disk_gib,omitempty"`
		FirewallID    string            `json:"firewall_id,omitempty"`
		Command       []string          `json:"command,omitempty"`
		NetworkID     string            `json:"network_id,omitempty"`
		RestartPolicy string            `json:"restart_policy,omitempty"`
		VCPU          int               `json:"vcpu,omitempty"`
		MemoryMiB     int               `json:"memory_mib,omitempty"`
		Group         string            `json:"placement_group,omitempty"`
		Strict        bool              `json:"placement_strict,omitempty"`
		NodeSelector  map[string]string `json:"node_selector,omitempty"`
		ExtraNetworks []string          `json:"extra_networks,omitempty"`
		Env           map[string]string `json:"env,omitempty"`
		Files         []fileSpec        `json:"files,omitempty"`
		Keys          []string          `json:"keys,omitempty"`
	}
	var (
		selectors []string
		envPairs  []string
		networks  []string
		filePairs []string
		drop      bool
	)

	cmd := &cobra.Command{
		Use:   "update <name|id> [-- command args...]",
		Short: "Publish a new revision and roll the replicas onto it",
		Long: "Publish a new revision and roll the replicas onto it.\n\n" +
			"A revision is the whole template, not a patch: whatever is not passed is not\n" +
			"carried over. Environment variables and files are sealed and never served back,\n" +
			"so the command cannot quietly bring them along - pass them again, or --drop to\n" +
			"say out loud that the new revision runs without them.\n\n" +
			"One replica is retired per pass and the loop makes its replacement, so a service\n" +
			"runs one short while a replica turns over and a revision that cannot start costs\n" +
			"one replica rather than all of them. Rolling back is publishing the old template\n" +
			"again, which is a new revision of its own.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Command = args[1:]

			if len(networks) > 0 {
				req.NetworkID = networks[0]
				req.ExtraNetworks = networks[1:]
			}

			for _, pair := range envPairs {
				name, value, found := strings.Cut(pair, "=")
				if !found {
					return errors.New("an environment variable is NAME=value, and " +
						pair + " has no value")
				}
				if req.Env == nil {
					req.Env = map[string]string{}
				}
				req.Env[name] = value
			}

			files, err := readFiles(filePairs)
			if err != nil {
				return err
			}
			req.Files = files

			for _, pair := range selectors {
				key, value, found := strings.Cut(pair, "=")
				if !found {
					return errors.New(
						"a node selector is key=value, and " + pair + " has no value")
				}
				if req.NodeSelector == nil {
					req.NodeSelector = map[string]string{}
				}
				req.NodeSelector[key] = value
			}

			var current serviceView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/services/"+args[0], nil, &current,
			); err != nil {
				return err
			}
			if err := checkCarried(current, len(req.Env), len(req.Files), drop); err != nil {
				return err
			}

			var updated serviceView
			if err := g.client().do(
				cmd.Context(), "PUT", "/v1/services/"+args[0]+"/template", req, &updated,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, updated, table{
				headers: serviceHeaders,
				rows:    [][]string{serviceRow(updated)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Isolation, "isolation", "container",
		"container, vm, microvm, or sandbox")
	cmd.Flags().StringVar(&req.Image, "image", "", "image every replica boots")
	cmd.Flags().StringVar(&req.ISO, "iso", "", "iso every replica boots")
	cmd.Flags().StringVar(&req.Kernel, "kernel", "", "kernel image for microvm and sandbox")
	cmd.Flags().IntVar(&req.DiskGiB, "disk-gib", 0, "root disk size for a vm")
	cmd.Flags().StringVar(&req.FirewallID, "firewall", "", "firewall every replica carries")
	cmd.Flags().StringVar(&req.RestartPolicy, "restart", "", "never, on-failure, or always")
	cmd.Flags().IntVar(&req.VCPU, "vcpu", 0, "virtual CPUs per replica")
	cmd.Flags().IntVar(&req.MemoryMiB, "memory-mib", 0, "memory per replica in MiB")
	cmd.Flags().StringArrayVar(&filePairs, "file", nil,
		"/path/in/the/workload=local-file[:mode] every replica gets, repeatable; sealed at rest")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil,
		"NAME=value every replica runs with, repeatable; sealed at rest and never served back")
	cmd.Flags().StringArrayVar(&networks, "network", nil,
		"network every replica joins, repeatable; the first is eth0")
	cmd.Flags().StringArrayVar(&selectors, "node-selector", nil,
		"only place replicas on nodes carrying key=value, repeatable and combined with and")
	cmd.Flags().StringVar(&req.Group, "placement-group", "",
		"spread replicas across zones and nodes")
	cmd.Flags().BoolVar(&req.Strict, "placement-strict", false,
		"refuse to place a replica that would break the spread")
	cmd.Flags().StringArrayVar(&req.Keys, "key", nil, "ssh key name, repeatable")
	cmd.Flags().BoolVar(&drop, "drop", false,
		"publish without the environment and files the current revision carries")

	return cmd
}

func checkCarried(current serviceView, env, files int, drop bool) error {
	if drop {
		return nil
	}

	var missing []string
	if len(current.EnvNames) > 0 && env == 0 {
		missing = append(missing, strconv.Itoa(len(current.EnvNames))+
			" environment variables ("+strings.Join(current.EnvNames, ", ")+")")
	}
	if len(current.FilePaths) > 0 && files == 0 {
		missing = append(missing, strconv.Itoa(len(current.FilePaths))+
			" files ("+strings.Join(current.FilePaths, ", ")+")")
	}
	if len(missing) == 0 {
		return nil
	}

	return errors.New("revision " + strconv.Itoa(current.Revision) + " of " + current.Name +
		" carries " + strings.Join(missing, " and ") + ", and a revision is the whole " +
		"template rather than a patch. These are sealed and never served back, so they " +
		"cannot be carried over for you: pass them again, or --drop to publish without them")
}

func newServiceDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a service and every replica it holds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/services/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s and its replicas\n", args[0])
			return nil
		},
	}
}
