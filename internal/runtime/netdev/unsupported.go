//go:build !linux

package netdev

import (
	"context"
	"errors"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

var errUnsupported = errors.New("the network datapath needs Linux")

func EnsureBridge(string, string) error {
	return errUnsupported
}

func EnsureEgress(string, string) error {
	return errUnsupported
}

func Attach(int, Interface) error {
	return errUnsupported
}

func Detach(string) error {
	return errUnsupported
}

func (Datapath) ApplyRoutes(context.Context, []workload.Route) error {
	return errUnsupported
}
