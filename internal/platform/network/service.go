package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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

	existing, err := s.repo.listNetworks(ctx)
	if err != nil {
		return Network{}, translate(err)
	}
	for _, other := range existing {
		taken, parseErr := parsePrefix(other.CIDR)
		if parseErr != nil {
			continue
		}
		if taken.Overlaps(prefix) {
			return Network{}, fault.Conflict("network_overlaps",
				"the range overlaps network "+other.Name+" ("+other.CIDR+"), and every node "+
					"routes to a peer slice by its prefix, so two networks cannot share one")
		}
	}

	id := ids.New("nw")
	n := Network{
		ID:        id,
		ProjectID: params.ProjectID,
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

func (s *service) nicOf(ctx context.Context, instanceID string) (NIC, error) {
	nic, err := s.repo.nic(ctx, instanceID)
	if err != nil {
		return NIC{}, translate(err)
	}
	return nic, nil
}

func (s *service) remove(ctx context.Context, id, projectID string) error {
	if _, err := s.getIn(ctx, id, projectID); err != nil {
		return err
	}

	attached, err := s.repo.countNICs(ctx, id)
	if err != nil {
		return translate(err)
	}
	if attached > 0 {
		return fault.Conflict("network_in_use",
			"the network still has addresses handed out to instances")
	}

	if err := s.repo.deleteNetwork(ctx, id); err != nil {
		return translate(err)
	}
	return nil
}

func (s *service) ensureDefaultFor(ctx context.Context, projectID string) (Network, error) {
	existing, err := s.repo.networkByName(ctx, projectID, DefaultName)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, errNotFound) {
		return Network{}, translate(err)
	}

	cidr, err := s.freeDefaultCIDR(ctx)
	if err != nil {
		return Network{}, err
	}
	return s.create(ctx, CreateParams{ProjectID: projectID, Name: DefaultName, CIDR: cidr})
}

func (s *service) freeDefaultCIDR(ctx context.Context) (string, error) {
	existing, err := s.repo.listNetworks(ctx)
	if err != nil {
		return "", translate(err)
	}

	taken := make([]netip.Prefix, 0, len(existing))
	for _, other := range existing {
		if prefix, parseErr := parsePrefix(other.CIDR); parseErr == nil {
			taken = append(taken, prefix)
		}
	}

	preferred, err := parsePrefix(DefaultCIDR)
	if err != nil {
		return "", fault.Internal(err)
	}
	if !overlapsAny(preferred, taken) {
		return preferred.String(), nil
	}

	supernet, err := parsePrefix(SupernetCIDR)
	if err != nil {
		return "", fault.Internal(err)
	}

	free, err := freePrefix(supernet, ProjectBits, taken)
	if err != nil {
		return "", fault.Conflict("address_space_exhausted", err.Error())
	}
	return free.String(), nil
}

func (s *service) listAll(ctx context.Context) ([]Network, error) {
	networks, err := s.repo.listNetworks(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return networks, nil
}

func (s *service) listIn(ctx context.Context, projectID string) ([]Network, error) {
	networks, err := s.repo.listNetworksIn(ctx, projectID)
	if err != nil {
		return nil, translate(err)
	}
	return networks, nil
}

func (s *service) getIn(ctx context.Context, id, projectID string) (Network, error) {
	n, err := s.get(ctx, id)
	if err != nil {
		return Network{}, err
	}
	if n.ProjectID != projectID {
		return Network{}, fault.NotFound("network_not_found", "no network with that id exists")
	}
	return n, nil
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
	held, err := s.repo.nicsOf(ctx, instanceID)
	if err != nil {
		return NIC{}, translate(err)
	}

	device := 0
	for _, n := range held {
		if n.NetworkID == networkID {
			return n, nil
		}
		if n.Device >= device {
			device = n.Device + 1
		}
	}
	if device >= MaxNICs {
		return NIC{}, fault.Conflict("too_many_nics", fmt.Sprintf(
			"an instance carries at most %d interfaces", MaxNICs))
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
		Device:     device,
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

func (s *service) allAddresses(ctx context.Context) (map[string]string, error) {
	addresses, err := s.repo.allAddresses(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return addresses, nil
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
		peers, err := s.repo.slicesExcept(ctx, networkID, nodeID)
		if err != nil {
			return nil, translate(err)
		}

		views = append(views, NodeNetwork{
			Network: n,
			Slice:   slice,
			NICs:    byNetwork[networkID],
			Peers:   peers,
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
