package cli

import (
	"github.com/spf13/cobra"
)

type deviceView struct {
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	Kind       string `json:"kind"`
	Vendor     string `json:"vendor"`
	Product    string `json:"product"`
	Driver     string `json:"driver"`
	Ready      bool   `json:"ready"`
	InstanceID string `json:"instance_id"`
}

type deviceListView struct {
	Devices []deviceView `json:"devices"`
}

var deviceHeaders = []string{"NODE", "ADDRESS", "KIND", "VENDOR:PRODUCT", "DRIVER", "GIVEN TO"}

func deviceRow(d deviceView) []string {
	given := d.InstanceID
	if given == "" {
		given = "free"
	}
	if !d.Ready {
		given = "held by " + d.Driver
	}

	return []string{
		d.NodeID,
		d.Address,
		d.Kind,
		d.Vendor + ":" + d.Product,
		d.Driver,
		given,
	}
}

func newDeviceCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "device",
		Short: "List the devices nodes can hand to a guest",
		Long: "List the devices nodes can hand to a guest.\n\n" +
			"Nodes report what they carry on every pass, so this is an inventory rather than\n" +
			"something anybody registers. A card only counts as available once it is bound\n" +
			"to vfio-pci: while a host driver holds it, qemu cannot open it, and placing a\n" +
			"workload on it would fail after the placement already happened.\n\n" +
			"One card goes to one workload. Deleting the workload gives it back.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list deviceListView
			if err := g.client().do(
				cmd.Context(), "GET", "/v1/devices", nil, &list,
			); err != nil {
				return err
			}

			rows := make([][]string, 0, len(list.Devices))
			for _, one := range list.Devices {
				rows = append(rows, deviceRow(one))
			}
			return render(cmd.OutOrStdout(), g.output, list,
				table{headers: deviceHeaders, rows: rows})
		},
	}
}
