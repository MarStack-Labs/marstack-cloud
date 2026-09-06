package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const docsHeader = `# CLI reference

Every command ` + "`marstack`" + ` accepts, generated from the binary itself with
` + "`make docs`" + `. If a command is here it exists, and if a flag is missing from here it
does not.

One binary is three things: ` + "`marstack server`" + ` is the control plane,
` + "`marstack agent`" + ` runs on each node, and every other command is the client talking to a
control plane over HTTP.

## Reaching a control plane

The client needs an endpoint and a token. Both can come from the environment, so they do not have
to be repeated on every command.

| Variable | Flag | Meaning |
|---|---|---|
| ` + "`MARSTACK_ENDPOINT`" + ` | ` + "`--endpoint`" + ` | control plane to talk to |
| ` + "`MARSTACK_TOKEN`" + ` | | bearer token to send |
| ` + "`MARSTACK_CA_FILE`" + ` | ` + "`--ca-file`" + ` | certificate authority to trust for https |

` + "```sh" + `
export MARSTACK_ENDPOINT=https://control.example.internal:7443
export MARSTACK_TOKEN=$(marstack login --email you@example.test)

marstack instance list
marstack instance list --output json
` + "```" + `

A control plane writes its first admin token to ` + "`<data-dir>/bootstrap-token`" + ` on the first
start. That token is how the first person signs in and creates everyone else.

`

func newDocsCmd() *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:    "docs",
		Short:  "Write the CLI reference",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := cmd.Root()

			var page strings.Builder
			page.WriteString(docsHeader)
			writeContents(&page, root)
			writeCommand(&page, root, 2)

			if dir := filepath.Dir(out); dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
			if err := os.WriteFile(out, []byte(page.String()), 0o644); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
			return nil
		},
	}

	cmd.Flags().StringVar(&out, "out", "docs/cli.md", "file to write the reference to")
	return cmd
}

func writeContents(page *strings.Builder, root *cobra.Command) {
	page.WriteString("## Commands\n\n")
	page.WriteString("| Command | What it does |\n|---|---|\n")

	for _, child := range documented(root) {
		page.WriteString(fmt.Sprintf("| [`%s`](#%s) | %s |\n",
			child.Name(), anchor(child.CommandPath()), child.Short))
	}
	page.WriteString("\n")
}

func writeCommand(page *strings.Builder, cmd *cobra.Command, depth int) {
	if cmd.HasParent() {
		level := depth
		if level > 6 {
			level = 6
		}

		page.WriteString(fmt.Sprintf("%s `%s`\n\n", strings.Repeat("#", level), cmd.CommandPath()))

		if cmd.Long != "" {
			page.WriteString(cmd.Long + "\n\n")
		} else if cmd.Short != "" {
			page.WriteString(cmd.Short + "\n\n")
		}

		page.WriteString("```\n" + cmd.UseLine() + "\n```\n\n")

		if cmd.Example != "" {
			page.WriteString("```sh\n" + strings.TrimSpace(cmd.Example) + "\n```\n\n")
		}

		writeFlags(page, cmd.NonInheritedFlags())
	}

	children := documented(cmd)
	if cmd.HasParent() && len(children) > 0 {
		page.WriteString("| Subcommand | What it does |\n|---|---|\n")
		for _, child := range children {
			page.WriteString(fmt.Sprintf("| [`%s`](#%s) | %s |\n",
				child.CommandPath(), anchor(child.CommandPath()), child.Short))
		}
		page.WriteString("\n")
	}

	for _, child := range children {
		writeCommand(page, child, depth+1)
	}
}

func writeFlags(page *strings.Builder, flags *pflag.FlagSet) {
	if !flags.HasAvailableFlags() {
		return
	}

	var rows strings.Builder
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}

		name := "`--" + f.Name + "`"
		if f.Shorthand != "" {
			name = "`-" + f.Shorthand + "`, " + name
		}

		kind := f.Value.Type()
		if kind == "bool" {
			kind = ""
		} else {
			kind = "`" + kind + "`"
		}

		fallback := ""
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "[]" {
			fallback = "`" + f.DefValue + "`"
		}

		rows.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			name, kind, fallback, oneLine(f.Usage)))
	})

	if rows.Len() == 0 {
		return
	}

	page.WriteString("| Flag | Type | Default | Meaning |\n|---|---|---|---|\n")
	page.WriteString(rows.String())
	page.WriteString("\n")
}

func documented(cmd *cobra.Command) []*cobra.Command {
	var kept []*cobra.Command
	for _, child := range cmd.Commands() {
		if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
			continue
		}
		kept = append(kept, child)
	}
	return kept
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "|", `\|`)), " ")
}

func anchor(path string) string {
	return strings.ReplaceAll(strings.ToLower(path), " ", "-")
}
