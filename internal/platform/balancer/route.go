package balancer

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

func normalizeRoutes(params []RouteParams) ([]RouteParams, error) {
	if len(params) > MaxRoutes {
		return nil, fault.Invalid("too_many_routes", fmt.Sprintf(
			"a balancer carries at most %d routes", MaxRoutes))
	}

	seen := map[string]bool{}
	out := make([]RouteParams, 0, len(params))

	for _, one := range params {
		one.Host = strings.ToLower(strings.TrimSpace(one.Host))
		one.Path = strings.TrimSpace(one.Path)

		if one.Service == "" {
			return nil, fault.Invalid("invalid_route",
				"every route names the service that answers it")
		}
		if one.Host != "" {
			if len(one.Host) > MaxHostLength || !hostPattern.MatchString(one.Host) {
				return nil, fault.Invalid("invalid_route",
					fmt.Sprintf("%q is not a host name", one.Host))
			}
		}
		if one.Path != "" {
			if !strings.HasPrefix(one.Path, "/") {
				return nil, fault.Invalid("invalid_route",
					"a path starts with a slash, and "+one.Path+" does not")
			}
			if len(one.Path) > MaxPathLength {
				return nil, fault.Invalid("invalid_route", fmt.Sprintf(
					"a path is at most %d characters", MaxPathLength))
			}
			one.Path = strings.TrimSuffix(one.Path, "/")
		}

		key := one.Host + "\x00" + one.Path
		if seen[key] {
			return nil, fault.Invalid("invalid_route",
				"two routes match "+describeMatch(one.Host, one.Path)+
					", and one request cannot have two answers")
		}
		seen[key] = true
		out = append(out, one)
	}
	return out, nil
}

func describeMatch(host, path string) string {
	if host == "" {
		host = "any host"
	}
	if path == "" {
		path = "/"
	}
	return host + path
}

func sortRoutes(routes []Route) {
	sort.SliceStable(routes, func(i, j int) bool {
		left, right := routes[i], routes[j]

		if (left.Host == "") != (right.Host == "") {
			return left.Host != ""
		}
		if len(left.Path) != len(right.Path) {
			return len(left.Path) > len(right.Path)
		}
		if left.Host != right.Host {
			return left.Host < right.Host
		}
		return left.Path < right.Path
	})
}

func (s *service) checkRoutes(ctx context.Context, projectID string,
	routes []RouteParams) error {
	if s.services == nil {
		return nil
	}
	for _, one := range routes {
		if _, err := s.services.MembersOf(ctx, projectID, one.Service); err != nil {
			return fault.Invalid("unknown_service",
				"no service named "+one.Service+" exists in this project, and a route to "+
					"nothing answers every request for "+
					describeMatch(one.Host, one.Path)+" with a 404")
		}
	}
	return nil
}

func (s *service) routeMembership(ctx context.Context, b Balancer) (Balancer, error) {
	if len(b.Routes) == 0 || s.services == nil {
		return b, nil
	}

	health, err := s.repo.healthOf(ctx, b.ID)
	if err != nil {
		return b, err
	}

	for i := range b.Routes {
		route := &b.Routes[i]

		ids, err := s.services.MembersOf(ctx, b.ProjectID, route.ServiceID)
		if err != nil {
			route.Backends = nil
			continue
		}

		backends := make([]Backend, 0, len(ids))
		for _, id := range ids {
			backend := Backend{InstanceID: id}
			if known, seen := health[id]; seen {
				backend.Probe = known.Probe
				backend.Reason = known.Reason
				backend.CheckedAt = known.CheckedAt
			}
			backends = append(backends, backend)
		}
		route.Backends = backends
	}

	sortRoutes(b.Routes)
	return b, nil
}

func (s *service) withRouteHealth(ctx context.Context, b Balancer) Balancer {
	if s.members == nil {
		return b
	}

	for i := range b.Routes {
		route := &b.Routes[i]
		held := Balancer{
			Family:   b.Family,
			Check:    b.Check,
			Backends: route.Backends,
		}
		route.Backends = s.withHealth(ctx, held).Backends
	}
	return b
}

func (s *service) everyBackend(ctx context.Context, b Balancer) ([]Backend, error) {
	held, err := s.membership(ctx, b)
	if err != nil {
		return nil, err
	}

	held, err = s.routeMembership(ctx, held)
	if err != nil {
		return nil, err
	}

	backends := held.Backends
	for _, route := range held.Routes {
		backends = append(backends, route.Backends...)
	}
	return backends, nil
}

func (s *service) setRoutes(
	ctx context.Context, id, projectID string, asked []RouteParams,
) (Balancer, error) {
	b, err := s.get(ctx, projectID, id)
	if err != nil {
		return Balancer{}, err
	}
	if len(b.Routes) == 0 {
		return Balancer{}, fault.Invalid("not_a_routing_balancer",
			"this balancer sends everything on its port to one set of backends, and turning "+
				"that into routing would change what the port answers with no way back")
	}

	routes, err := normalizeRoutes(asked)
	if err != nil {
		return Balancer{}, err
	}
	if len(routes) == 0 {
		return Balancer{}, fault.Invalid("no_routes",
			"a routing balancer with no routes is a port that answers every request with a "+
				"404: delete it instead")
	}
	if err := s.checkRoutes(ctx, projectID, routes); err != nil {
		return Balancer{}, err
	}

	replaced := make([]Route, 0, len(routes))
	for _, one := range routes {
		replaced = append(replaced, Route{
			Host:      one.Host,
			Path:      one.Path,
			ServiceID: one.Service,
		})
	}

	if err := s.repo.replaceRoutes(ctx, b.ID, replaced); err != nil {
		return Balancer{}, translate(err)
	}

	b.Routes = replaced
	return s.resolve(ctx, b)
}

func (s *service) hostsOf(ctx context.Context, id, projectID string) ([]string, error) {
	b, err := s.get(ctx, projectID, id)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	hosts := make([]string, 0, len(b.Routes))
	for _, route := range b.Routes {
		if route.Host == "" || seen[route.Host] {
			continue
		}
		seen[route.Host] = true
		hosts = append(hosts, route.Host)
	}

	if len(hosts) == 0 {
		return nil, fault.Invalid("no_hosts",
			"this balancer names no host, so there is nothing to get a certificate for: "+
				"give it routes with a host, or name the hosts yourself")
	}
	return hosts, nil
}
