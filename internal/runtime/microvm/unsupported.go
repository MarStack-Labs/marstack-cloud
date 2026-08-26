//go:build !linux

package microvm

import (
	"context"
	"errors"
	"log/slog"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

var errUnsupported = errors.New("the microvm runtime needs Linux with KVM")

type VMM interface {
	Binary() string
	Name() string
	HasSerialSocket() bool
	ConsoleDevice() string
}

type Images interface {
	Stage(ctx context.Context, reference, kind string) (string, error)
}

type Runtime struct{}

func New(_ string, _ *slog.Logger, _ VMM, _ Images) *Runtime {
	return &Runtime{}
}

func CloudHypervisor() VMM {
	return nil
}

func Firecracker() VMM {
	return nil
}

func (r *Runtime) Name() string {
	return "microvm"
}

func (r *Runtime) List(context.Context) ([]string, error) {
	return nil, errUnsupported
}

func (r *Runtime) Start(context.Context, workload.Spec) error {
	return errUnsupported
}

func (r *Runtime) Stop(context.Context, string) error {
	return errUnsupported
}

func (r *Runtime) Status(context.Context, string) (workload.State, error) {
	return workload.State{}, errUnsupported
}

func (r *Runtime) Remove(context.Context, string) error {
	return errUnsupported
}
