//go:build !linux

package container

import (
	"context"
	"errors"
	"log/slog"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

var errUnsupported = errors.New("the container runtime needs Linux namespaces and cgroup v2")

type Runtime struct{}

func New(_ string, _ *slog.Logger) *Runtime {
	return &Runtime{}
}

func (r *Runtime) Name() string {
	return "container"
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

func RunInit() error {
	return errUnsupported
}
