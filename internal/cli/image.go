package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type imageView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Arch      string   `json:"arch"`
	Source    string   `json:"source"`
	Checksum  string   `json:"checksum,omitempty"`
	SizeBytes int64    `json:"size_bytes,omitempty"`
	Nodes     []string `json:"nodes,omitempty"`
	CreatedAt string   `json:"created_at"`
}

type imageListView struct {
	Images []imageView `json:"images"`
}

var imageHeaders = []string{"NAME", "ID", "KIND", "ARCH", "SIZE", "NODES", "SOURCE"}

func imageRow(in imageView) []string {
	size := "-"
	if in.SizeBytes > 0 {
		size = strconv.FormatInt(in.SizeBytes/(1024*1024), 10) + "Mi"
	}

	source := in.Source
	if len(source) > 44 {
		source = source[:41] + "..."
	}

	nodes := "-"
	if len(in.Nodes) > 0 {
		nodes = strconv.Itoa(len(in.Nodes))
	}

	return []string{in.Name, in.ID, in.Kind, in.Arch, size, nodes, source}
}

func newImageCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "image",
		Short:   "Manage the image catalog",
		Aliases: []string{"images"},
	}
	cmd.AddCommand(
		newImageCreateCmd(g),
		newImageListCmd(g),
		newImageGetCmd(g),
		newImageDeleteCmd(g),
	)
	return cmd
}

func newImageCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Arch     string `json:"arch,omitempty"`
		Source   string `json:"source"`
		Checksum string `json:"checksum,omitempty"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Register an image a node can download",
		Long: "Register an image a node can download.\n\n" +
			"kind disk is a bootable cloud image for isolation vm, kind iso is optical media\n" +
			"to attach to one, and kind kernel is the uncompressed kernel a microvm boots.\n" +
			"Container, microvm and sandbox root filesystems come from an OCI reference on the\n" +
			"instance itself and are not registered here.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created imageView
			if err := g.client().do(cmd.Context(), "POST", "/v1/images", req, &created); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, created, table{
				headers: imageHeaders,
				rows:    [][]string{imageRow(created)},
			})
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "image name, unique within the platform")
	cmd.Flags().StringVar(&req.Kind, "kind", "disk", "disk, iso or kernel")
	cmd.Flags().StringVar(&req.Arch, "arch", "", "arm64 or amd64, defaults to arm64")
	cmd.Flags().StringVar(&req.Source, "source", "", "http or https URL a node downloads from")
	cmd.Flags().StringVar(&req.Checksum, "checksum", "",
		"sha256:<hex>, verified after download and pinned for later checks")
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("source"))

	return cmd
}

func newImageListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list imageListView
			if err := g.client().do(cmd.Context(), "GET", "/v1/images", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Images))
			for _, in := range list.Images {
				rows = append(rows, imageRow(in))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: imageHeaders, rows: rows})
		},
	}
}

func newImageGetCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|id>",
		Short: "Show one image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var in imageView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/images/"+args[0], nil, &in,
			); err != nil {
				return err
			}
			return render(cmd.OutOrStdout(), g.output, in, table{
				headers: imageHeaders,
				rows:    [][]string{imageRow(in)},
			})
		},
	}
}

func newImageDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Remove an image from the catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.client().do(
				cmd.Context(), "DELETE", "/v1/images/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}
