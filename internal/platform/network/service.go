package network

import (
	"context"
	"errors"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

type clock func() time.Time

type service struct {
	repo *repository
	now  clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Network, error) {
	if err := validate.Name("name", params.Name); err != nil {
		return Network{}, err
	}
	if params.CIDR == "" {
		params.CIDR = DefaultCIDR
	}

	prefix, err := parsePrefix(params.CIDR)
	if err != nil {
		return Network{}, fault.Invalid("invalid_cidr", err.Error())
	}
	if prefix.Bits() >= SliceBits {
		return Network{}, fault.Invalid("invalid_cidr",
			"the network must be larger than a single node slice")
	}

	id := ids.New("nw")
	n := Network{
		ID:        id,
		Name:      params.Name,
		CIDR:      prefix.String(),
		Gateway:   gatewayOf(prefix).String(),
		Bridge:    bridgeName(id),
		CreatedAt: s.now(),
	}

	if err := s.repo.insertNetwork(ctx, n); err != nil {
		return Network{}, translate(err)
	}
	return n, nil
}

func (s *service) ensureDefault(ctx context.Context) (Network, error) {
	existing, err := s.repo.networkByName(ctx, DefaultName)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, errNotFound) {
		return Network{}, translate(err)
	}
	return s.create(ctx, CreateParams{Name: DefaultName, CIDR: DefaultCIDR})
}

func (s *service) list(ctx context.Context) ([]Network, error) {
	networks, err := s.repo.listNetworks(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return networks, nil
}

func (s *service) get(ctx context.Context, id string) (Network, error) {
	n, err := s.repo.network(ctx, id)
	if err != nil {
		return Network{}, translate(err)
	}
	return n, nil
}

func (s *service) ensureSlice(ctx context.Context, networkID, nodeID string) (Slice, error) {
	if existing, err := s.repo.slice(ctx, networkID, nodeID); err == nil {
		return existing, nil
	} else if !errors.Is(err, errNotFound) {
		return Slice{}, translate(err)
	}

	n, err := s.get(ctx, networkID)
	if err != nil {
		return Slice{}, err
	}

	prefix, err := parsePrefix(n.CIDR)
	if err != nil {
		return Slice{}, fault.Internal(err)
	}

	taken, err := s.repo.takenSlices(ctx, networkID)
	if err != nil {
		return Slice{}, translate(err)
	}

	candidate, err := nextSlice(prefix, taken)
	if err != nil {
		return Slice{}, fault.Conflict("network_exhausted", err.Error())
	}

	slice := Slice{
		NetworkID: networkID,
		NodeID:    nodeID,
		CIDR:      candidate.String(),
		CreatedAt: s.now(),
	}
	if err := s.repo.insertSlice(ctx, slice); err != nil {
		if errors.Is(err, errCIDRTaken) {
			return s.repo.slice(ctx, networkID, nodeID)
		}
		return Slice{}, translate(err)
	}
	return slice, nil
}

func (s *service) allocate(ctx context.Context, instanceID, networkID, nodeID string) (NIC, error) {
	if existing, err := s.repo.nic(ctx, instanceID); err == nil {
		return existing, nil
	} else if !errors.Is(err, errNotFound) {
		return NIC{}, translate(err)
	}

	slice, err := s.ensureSlice(ctx, networkID, nodeID)
	if err != nil {
		return NIC{}, err
	}

	prefix, err := parsePrefix(slice.CIDR)
	if err != nil {
		return NIC{}, fault.Internal(err)
	}

	taken, err := s.repo.takenAddresses(ctx, networkID)
	if err != nil {
		return NIC{}, translate(err)
	}

	address, err := nextAddress(prefix, taken)
	if err != nil {
		return NIC{}, fault.Conflict("slice_exhausted", err.Error())
	}

	mac, err := randomMAC()
	if err != nil {
		return NIC{}, fault.Internal(err)
	}

	nic := NIC{
		InstanceID: instanceID,
		NetworkID:  networkID,
		NodeID:     nodeID,
		IP:         address.String(),
		MAC:        mac,
		CreatedAt:  s.now(),
	}
	if err := s.repo.insertNIC(ctx, nic); err != nil {
		return NIC{}, translate(err)
	}
	return nic, nil
}

func (s *service) release(ctx context.Context, instanceID string) error {
	return translate(s.repo.deleteNIC(ctx, instanceID))
}

func (s *service) nodeView(ctx context.Context, nodeID string) ([]NodeNetwork, error) {
	nics, err := s.repo.nicsOnNode(ctx, nodeID)
	if err != nil {
		return nil, translate(err)
	}

	byNetwork := map[string][]NIC{}
	order := make([]string, 0)
	for _, nic := range nics {
		if _, seen := byNetwork[nic.NetworkID]; !seen {
			order = append(order, nic.NetworkID)
		}
		byNetwork[nic.NetworkID] = append(byNetwork[nic.NetworkID], nic)
	}

	views := make([]NodeNetwork, 0, len(order))
	for _, networkID := range order {
		n, err := s.get(ctx, networkID)
		if err != nil {
			return nil, err
		}
		slice, err := s.ensureSlice(ctx, networkID, nodeID)
		if err != nil {
			return nil, err
		}
		views = append(views, NodeNetwork{
			Network: n,
			Slice:   slice,
			NICs:    byNetwork[networkID],
		})
	}
	return views, nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("network_not_found", "no network with that id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("network_name_taken", "a network with that name already exists")
	case errors.Is(err, errCIDRTaken):
		return fault.Conflict("address_taken", "that address was allocated by another request")
	default:
		return fault.Internal(err)
	}
}
