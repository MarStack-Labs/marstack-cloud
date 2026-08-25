//go:build !linux

package qemu

import (
	"context"
	"errors"
	"log/slog"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

var errUnsupported = errors.New("the vm runtime needs Linux with KVM")

type Runtime struct{}

func New(_ string, _ *slog.Logger) *Runtime {
	return &Runtime{}
}

func (r *Runtime) Name() string {
	return "vm"
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
