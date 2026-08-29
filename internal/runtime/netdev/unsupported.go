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

func TapName(string) string {
	return ""
}

func EnsureTap(string, string) error {
	return errUnsupported
}

func DeleteLink(string) error {
	return errUnsupported
}

func (Datapath) ApplyEgress(context.Context, []workload.Egress) error {
	return errUnsupported
}

func (Datapath) ApplyRoutes(context.Context, []workload.Route) error {
	return errUnsupported
}

func (Datapath) ApplyForwards(context.Context, []workload.Publish) error {
	return errUnsupported
}

func (Datapath) ApplyGuards(context.Context, []workload.Guard) error {
	return errUnsupported
}

func (Datapath) ApplyFilters(context.Context, []workload.Filter) error {
	return errUnsupported
}

func (Datapath) Prune(context.Context, workload.Keep) error {
	return errUnsupported
}
