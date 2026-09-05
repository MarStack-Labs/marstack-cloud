package node

import (
	"context"
	"fmt"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

const MaxDevicesPerNode = 64

type Device struct {
	NodeID     string
	Address    string
	Kind       string
	Vendor     string
	Product    string
	Driver     string
	Ready      bool
	InstanceID string
}

func (s *service) setDevices(ctx context.Context, nodeID string, held []Device) error {
	if len(held) > MaxDevicesPerNode {
		return fault.Invalid("too_many_devices", fmt.Sprintf(
			"a node reports at most %d devices", MaxDevicesPerNode))
	}

	for i := range held {
		held[i].NodeID = nodeID
		held[i].Address = strings.TrimSpace(held[i].Address)
		if held[i].Address == "" {
			return fault.Invalid("invalid_device", "a device with no address is not one")
		}
	}
	return s.repo.replaceDevices(ctx, nodeID, held)
}

func (s *service) devices(ctx context.Context) ([]Device, error) {
	return s.repo.devices(ctx)
}

func (s *service) FreeDevice(
	ctx context.Context, nodeID, kind string,
) (string, bool, error) {
	held, err := s.repo.devicesOn(ctx, nodeID)
	if err != nil {
		return "", false, err
	}

	for _, one := range held {
		if one.Kind == kind && one.Ready && one.InstanceID == "" {
			return one.Address, true, nil
		}
	}
	return "", false, nil
}

func (s *service) ClaimDevice(
	ctx context.Context, nodeID, kind, instanceID string,
) (string, error) {
	held, err := s.repo.devicesOn(ctx, nodeID)
	if err != nil {
		return "", err
	}

	for _, one := range held {
		if one.InstanceID == instanceID {
			return one.Address, nil
		}
	}

	for _, one := range held {
		if one.Kind != kind || !one.Ready || one.InstanceID != "" {
			continue
		}
		taken, err := s.repo.claimDevice(ctx, nodeID, one.Address, instanceID)
		if err != nil {
			return "", err
		}
		if taken {
			return one.Address, nil
		}
	}
	return "", fault.Conflict("no_free_device",
		"every "+kind+" on "+nodeID+" is already given to another workload")
}

func (s *service) ReleaseInstance(ctx context.Context, instanceID string) error {
	return s.repo.releaseDevice(ctx, instanceID)
}

func (s *service) DevicesOf(ctx context.Context, instanceID string) ([]Device, error) {
	return s.repo.devicesOfInstance(ctx, instanceID)
}
