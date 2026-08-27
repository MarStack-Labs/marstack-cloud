package forward

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type Addresses interface {
	Endpoint(ctx context.Context, instanceID string) (Endpoint, error)
}

type clock func() time.Time

type service struct {
	repo      *repository
	addresses Addresses
	now       clock
}

func newService(repo *repository, addresses Addresses, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, addresses: addresses, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Forward, error) {
	if params.InstanceID == "" {
		return Forward{}, fault.Invalid("invalid_instance", "instance_id must not be empty")
	}
	if params.Protocol == "" {
		params.Protocol = ProtocolTCP
	}
	if err := validate.OneOf("protocol", params.Protocol, Protocols()...); err != nil {
		return Forward{}, err
	}
	if params.TargetPort < MinPort || params.TargetPort > MaxPort {
		return Forward{}, fault.Invalid("invalid_target_port", fmt.Sprintf(
			"target_port must be between %d and %d", MinPort, MaxPort))
	}
	if params.NodePort == 0 {
		params.NodePort = params.TargetPort
	}
	if params.NodePort < MinPort || params.NodePort > MaxPort {
		return Forward{}, fault.Invalid("invalid_node_port", fmt.Sprintf(
			"node_port must be between %d and %d", MinPort, MaxPort))
	}

	if s.addresses == nil {
		return Forward{}, fault.Unavailable("addresses_unavailable",
			"the platform cannot look up where an instance runs")
	}

	endpoint, err := s.addresses.Endpoint(ctx, params.InstanceID)
	if err != nil {
		return Forward{}, err
	}
	if endpoint.ProjectID != params.ProjectID {
		return Forward{}, fault.NotFound("instance_not_found", "no instance with that id exists")
	}
	if endpoint.NodeID == "" || endpoint.Address == "" {
		return Forward{}, fault.Conflict("instance_unreachable",
			"the instance has no address yet, so there is nothing to publish")
	}

	f := Forward{
		ID:         ids.New("fwd"),
		ProjectID:  params.ProjectID,
		InstanceID: params.InstanceID,
		Protocol:   params.Protocol,
		NodePort:   params.NodePort,
		TargetPort: params.TargetPort,
		NodeID:     endpoint.NodeID,
		Address:    endpoint.Address,
		CreatedAt:  s.now(),
	}

	if err := s.repo.insert(ctx, f); err != nil {
		if errors.Is(err, errPortUsed) {
			return Forward{}, fault.Conflict("node_port_taken",
				"port "+strconv.Itoa(f.NodePort)+"/"+f.Protocol+" is already published on that node")
		}
		return Forward{}, translate(err)
	}
	return f, nil
}

func (s *service) onNode(ctx context.Context, nodeID string) ([]Forward, error) {
	forwards, err := s.repo.onNode(ctx, nodeID)
	if err != nil {
		return nil, translate(err)
	}
	return forwards, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Forward, error) {
	forwards, err := s.repo.listIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return forwards, nil
}

func (s *service) remove(ctx context.Context, id, projectID string) error {
	f, err := s.repo.byID(ctx, id)
	if err != nil {
		return translate(err)
	}
	if f.ProjectID != projectID {
		return fault.NotFound("forward_not_found", "no published port with that id exists")
	}
	return s.removeAny(ctx, f.ID)
}

func (s *service) removeAny(ctx context.Context, id string) error {
	if err := s.repo.delete(ctx, id); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) releaseInstance(ctx context.Context, instanceID string) error {
	if err := s.repo.deleteInstance(ctx, instanceID); err != nil {
		return translate(err)
	}
	return nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("forward_not_found", "no published port with that id exists")
	case errors.Is(err, errPortUsed):
		return fault.Conflict("node_port_taken", "that node port is already published")
	default:
		return err
	}
}
