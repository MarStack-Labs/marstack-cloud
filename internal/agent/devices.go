package agent

import (
	"context"

	"github.com/marstack-labs/marstack-cloud/internal/runtime/pcidev"
)

func (a *Agent) reportDevices(ctx context.Context) {
	nodeID := a.currentNodeID()
	if nodeID == "" {
		return
	}

	held, err := pcidev.Scan("")
	if err != nil {
		a.log.Warn("could not read the devices of this node", "error", err)
		return
	}

	reports := make([]deviceBody, 0, len(held))
	for _, one := range held {
		reports = append(reports, deviceBody{
			Address: one.Address,
			Kind:    one.Kind,
			Vendor:  one.Vendor,
			Product: one.Product,
			Driver:  one.Driver,
			Ready:   one.Ready,
		})
	}

	if err := a.client.reportDevices(ctx, nodeID, reports); err != nil {
		a.log.Warn("could not report the devices of this node", "error", err)
	}
}
