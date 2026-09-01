package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type jobRunView struct {
	ID        string `json:"id"`
	Attempt   int    `json:"attempt"`
	State     string `json:"state"`
	Message   string `json:"message"`
	ExitCode  *int   `json:"exit_code"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

type jobView struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Image   string       `json:"image"`
	Every   string       `json:"every"`
	Retries int          `json:"retries"`
	Paused  bool         `json:"paused"`
	NextAt  string       `json:"next_at"`
	Runs    []jobRunView `json:"runs"`
}

type jobListView struct {
	Jobs []jobView `json:"jobs"`
}

var jobHeaders = []string{"NAME", "ID", "IMAGE", "EVERY", "RETRIES", "STATE"}

func jobRow(j jobView) []string {
	schedule := j.Every
	if schedule == "" {
		schedule = "on demand"
	}

	state := "idle"
	for _, run := range j.Runs {
		if run.State == "pending" || run.State == "running" {
			state = "run " + run.State
		}
	}
	if state == "idle" && len(j.Runs) > 0 {
		state = "last " + j.Runs[len(j.Runs)-1].State
	}
	if j.Paused {
		state = "paused"
	}

	return []string{j.Name, j.ID, j.Image, schedule, strconv.Itoa(j.Retries), state}
}

var jobRunHeaders = []string{"RUN", "ATTEMPT", "STATE", "EXIT", "STARTED", "NOTE"}

func jobRunRow(run jobRunView) []string {
	exit := "-"
	if run.ExitCode != nil {
		exit = strconv.Itoa(*run.ExitCode)
	}
	return []string{
		run.ID,
		strconv.Itoa(run.Attempt),
		run.State,
		exit,
		run.StartedAt[:min(len(run.StartedAt), 19)],
		shortenLine(run.Message, 40),
	}
}

func newJobCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "job",
		Short:   "Run a workload to completion, once or on a schedule",
		Aliases: []string{"jobs"},
	}
	cmd.AddCommand(
		newJobCreateCmd(g),
		newJobListCmd(g),
		newJobGetCmd(g),
		newJobRunCmd(g),
		newJobPauseCmd(g),
		newJobResumeCmd(g),
		newJobDeleteCmd(g),
	)
	return cmd
}

func newJobCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name         string            `json:"name"`
		Isolation    string            `json:"isolation,omitempty"`
		Image        string            `json:"image,omitempty"`
		Kernel       string            `json:"kernel,omitempty"`
		Command      []string          `json:"command,omitempty"`
		NetworkID    string            `json:"network_id,omitempty"`
		FirewallID   string            `json:"firewall_id,omitempty"`
		VCPU         int               `json:"vcpu,omitempty"`
		MemoryMiB    int               `json:"memory_mib,omitempty"`
		NodeSelector map[string]string `json:"node_selector,omitempty"`
		Env          map[string]string `json:"env,omitempty"`
		Files        []fileSpec        `json:"files,omitempty"`
		Every        string            `json:"every,omitempty"`
		Retries      int               `json:"retries,omitempty"`
		Keep         int               `json:"keep,omitempty"`
	}
	var (
		envPairs  []string
		filePairs []string
		selectors []string
	)

	cmd := &cobra.Command{
		Use:   "create -- command args...",
		Short: "Create a job that runs a workload to completion",
		Long: "Create a job that runs a workload to completion.\n\n" +
			"A run finishes when its workload exits: zero is a success, anything else is a\n" +
			"failure, and a failure is retried up to --retries times before the job gives up.\n" +
			"The workload of a finished run is removed, so a nightly job does not leave one\n" +
			"behind every night.\n\n" +
			"A run always carries a restart policy of never, whatever the image would\n" +
			"otherwise do: a node that restarts the workload means the run never ends.\n\n" +
			"With --every the job also runs on a schedule. Two runs of one job never overlap;\n" +
			"if a run is still going when the next is due, that turn is skipped and recorded\n" +
			"rather than stacked.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Command = args

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

			var created jobView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/jobs", req, &created,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: jobHeaders,
				rows:    [][]string{jobRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "job name, unique in the project")
	cmd.Flags().StringVar(&req.Isolation, "isolation", "container",
		"container, vm, microvm, or sandbox")
	cmd.Flags().StringVar(&req.Image, "image", "", "image each run boots")
	cmd.Flags().StringVar(&req.Kernel, "kernel", "", "kernel image for microvm and sandbox")
	cmd.Flags().StringVar(&req.NetworkID, "network", "", "network each run joins")
	cmd.Flags().StringVar(&req.FirewallID, "firewall", "", "firewall each run carries")
	cmd.Flags().IntVar(&req.VCPU, "vcpu", 0, "virtual CPUs per run")
	cmd.Flags().IntVar(&req.MemoryMiB, "memory-mib", 0, "memory per run in MiB")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil,
		"NAME=value each run gets, repeatable; sealed at rest and never served back")
	cmd.Flags().StringArrayVar(&filePairs, "file", nil,
		"/path/in/the/workload=local-file[:mode] each run gets, repeatable; sealed at rest")
	cmd.Flags().StringArrayVar(&selectors, "node-selector", nil,
		"only place runs on nodes carrying key=value, repeatable")
	cmd.Flags().StringVar(&req.Every, "every", "",
		"run on this interval as well, for example 1h or 24h")
	cmd.Flags().IntVar(&req.Retries, "retries", 0, "how many times to retry a failed run")
	cmd.Flags().IntVar(&req.Keep, "keep", 0, "how many finished runs to remember")
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("image"))

	return cmd
}

func newJobListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List jobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list jobListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/jobs", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Jobs))
			for _, j := range list.Jobs {
				rows = append(rows, jobRow(j))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: jobHeaders, rows: rows})
		},
	}
}

func newJobGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|id>",
		Short: "Show a job and the runs it remembers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var j jobView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/jobs/"+args[0], nil, &j,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(j.Runs))
			for _, run := range j.Runs {
				rows = append(rows, jobRunRow(run))
			}
			return render(cmd.OutOrStdout(), g.output, j,
				table{headers: jobRunHeaders, rows: rows})
		},
	}
}

func newJobRunCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "run <name|id>",
		Short: "Start a run now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var run jobRunView
			if err := g.client().do(
				cmd.Context(), "POST", "/v1/jobs/"+args[0]+"/run", nil, &run,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, run, table{
				headers: jobRunHeaders,
				rows:    [][]string{jobRunRow(run)},
			})
		},
	}
}

func newJobPauseCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name|id>",
		Short: "Stop a scheduled job firing until it is resumed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendJobAction(cmd, g, args[0], "pause")
		},
	}
}

func newJobResumeCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name|id>",
		Short: "Let a paused job fire again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendJobAction(cmd, g, args[0], "resume")
		},
	}
}

func sendJobAction(cmd *cobra.Command, g *globals, id, action string) error {
	var changed jobView
	if err := g.client().do(
		cmd.Context(), "POST", "/v1/jobs/"+id+"/"+action, nil, &changed,
	); err != nil {
		return err
	}
	return render(cmd.OutOrStdout(), g.output, changed, table{
		headers: jobHeaders,
		rows:    [][]string{jobRow(changed)},
	})
}

func newJobDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a job, its run history and any workload still running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/jobs/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s and its runs\n", args[0])
			return nil
		},
	}
}
