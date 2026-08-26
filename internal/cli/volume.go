package cli

import (
	"strconv"

	"github.com/spf13/cobra"
)

type volumeView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SizeGiB    int    `json:"size_gib"`
	State      string `json:"state"`
	NodeID     string `json:"node_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type volumeListView struct {
	Volumes []volumeView `json:"volumes"`
}

var volumeHeaders = []string{"NAME", "ID", "SIZE", "STATE", "NODE", "INSTANCE"}

func volumeRow(v volumeView) []string {
	node := v.NodeID
	if node == "" {
		node = "-"
	}
	instance := v.InstanceID
	if instance == "" {
		instance = "-"
	}

	return []string{v.Name, v.ID, strconv.Itoa(v.SizeGiB) + "Gi", v.State, node, instance}
}

func newVolumeCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Short:   "Manage volumes",
		Aliases: []string{"volumes"},
	}
	cmd.AddCommand(
		newVolumeCreateCmd(g),
		newVolumeListCmd(g),
		newVolumeAttachCmd(g),
		newVolumeDetachCmd(g),
		newVolumeDeleteCmd(g),
	)
	return cmd
}

func newVolumeCreateCmd(g *globals) *cobra.Command {
	var req struct {
		Name    string `json:"name"`
		SizeGiB int    `json:"size_gib"`
	}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a volume, which lives on the node it is first attached to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var created volumeView
			if err := newClient(g.endpoint).do(cmd.Context(), "POST", "/v1/volumes", req, &created); err != nil {
				return err
			}
			return renderVolume(cmd, g, created)
		},
	}

	cmd.Flags().StringVar(&req.Name, "name", "", "volume name, unique within the platform")
	cmd.Flags().IntVar(&req.SizeGiB, "size-gib", 0, "size in GiB")
	must(cmd.MarkFlagRequired("name"))
	must(cmd.MarkFlagRequired("size-gib"))

	return cmd
}

func newVolumeListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List volumes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list volumeListView
			if err := newClient(g.endpoint).do(cmd.Context(), "GET", "/v1/volumes", nil, &list); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Volumes))
			for _, v := range list.Volumes {
				rows = append(rows, volumeRow(v))
			}
			return render(cmd.OutOrStdout(), g.output, list, table{headers: volumeHeaders, rows: rows})
		},
	}
}

func newVolumeAttachCmd(g *globals) *cobra.Command {
	var body struct {
		InstanceID string `json:"instance_id"`
	}

	cmd := &cobra.Command{
		Use:   "attach <name|id>",
		Short: "Attach a volume to an instance already placed on a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var attached volumeView
			if err := newClient(g.endpoint).do(
				cmd.Context(), "POST", "/v1/volumes/"+args[0]+"/attach", body, &attached,
			); err != nil {
				return err
			}
			cmd.PrintErrln("the instance picks the disk up on its next start")
			return renderVolume(cmd, g, attached)
		},
	}

	cmd.Flags().StringVar(&body.InstanceID, "instance", "", "instance id the volume attaches to")
	must(cmd.MarkFlagRequired("instance"))

	return cmd
}

func newVolumeDetachCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "detach <name|id>",
		Short: "Detach a volume from its instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var detached volumeView
			if err := newClient(g.endpoint).do(
				cmd.Context(), "POST", "/v1/volumes/"+args[0]+"/detach", nil, &detached,
			); err != nil {
				return err
			}
			return renderVolume(cmd, g, detached)
		},
	}
}

func newVolumeDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a volume and the data on it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := newClient(g.endpoint).do(
				cmd.Context(), "DELETE", "/v1/volumes/"+args[0], nil, nil,
			); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}
}

func renderVolume(cmd *cobra.Command, g *globals, v volumeView) error {
	return render(cmd.OutOrStdout(), g.output, v, table{
		headers: volumeHeaders,
		rows:    [][]string{volumeRow(v)},
	})
}
