package tlsproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second

	challengePrefix = "/.well-known/acme-challenge/"
)

type EndpointRoute struct {
	Host    string
	Path    string
	Targets []string
}

type targetKey struct{}

type compiledRoute struct {
	host    string
	path    string
	targets []string
	next    atomic.Uint64
}

func (r *compiledRoute) pick() (string, bool) {
	if len(r.targets) == 0 {
		return "", false
	}

	index := r.next.Add(1) - 1
	return r.targets[index%uint64(len(r.targets))], true
}

type routeTable struct {
	routes []*compiledRoute
}

func compileRoutes(routes []EndpointRoute) *routeTable {
	table := &routeTable{routes: make([]*compiledRoute, 0, len(routes))}
	for _, one := range routes {
		table.routes = append(table.routes, &compiledRoute{
			host:    strings.ToLower(strings.TrimSpace(one.Host)),
			path:    strings.TrimSuffix(one.Path, "/"),
			targets: append([]string(nil), one.Targets...),
		})
	}

	sort.SliceStable(table.routes, func(i, j int) bool {
		left, right := table.routes[i], table.routes[j]

		if (left.host == "") != (right.host == "") {
			return left.host != ""
		}
		if len(left.path) != len(right.path) {
			return len(left.path) > len(right.path)
		}
		if left.host != right.host {
			return left.host < right.host
		}
		return left.path < right.path
	})
	return table
}

func (t *routeTable) match(host, path string) (*compiledRoute, bool) {
	host = hostOnly(host)

	for _, one := range t.routes {
		if one.host != "" && one.host != host {
			continue
		}
		if !underPath(path, one.path) {
			continue
		}
		return one, true
	}
	return nil, false
}

func hostOnly(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if bare, _, err := net.SplitHostPort(host); err == nil {
		return bare
	}
	return host
}

func underPath(path, prefix string) bool {
	if prefix == "" {
		return true
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func routesFingerprint(routes []EndpointRoute) string {
	sum := sha256.New()
	for _, one := range routes {
		sum.Write([]byte(one.Host + "\x00" + one.Path + "\x00"))
		for _, target := range one.Targets {
			sum.Write([]byte(target + "\x00"))
		}
		sum.Write([]byte("\x01"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (m *Manager) router(id string, l *listener) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			address, _ := r.In.Context().Value(targetKey{}).(string)

			r.SetXForwarded()
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = address
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m.log.Warn("a routed request could not be delivered", "balancer", id,
				"host", hostOnly(r.Host), "path", r.URL.Path, "error", err)
			http.Error(w, "the backend did not answer", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, challengePrefix) {
			answer, held := m.challenge(r.URL.Path)
			if !held {
				http.Error(w, "this node holds no such challenge", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(answer))
			return
		}

		table := l.table.Load()
		if table == nil {
			http.Error(w, "this balancer has no routes", http.StatusServiceUnavailable)
			return
		}

		route, matched := table.match(r.Host, r.URL.Path)
		if !matched {
			http.Error(w, "no route matches this host and path", http.StatusNotFound)
			return
		}

		target, up := route.pick()
		if !up {
			http.Error(w, "no backend of this route is up", http.StatusServiceUnavailable)
			return
		}

		address := net.JoinHostPort(target, strconv.Itoa(l.targetPort))
		proxy.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), targetKey{}, address)))
	})
}

func (m *Manager) serveRoutes(id string, l *listener) {
	defer close(l.done)

	if err := l.server.Serve(l.net); err != nil && !errors.Is(err, http.ErrServerClosed) {
		m.log.Warn("routing listener stopped accepting", "balancer", id, "error", err)
	}
}

func (m *Manager) UseChallenges(answers map[string]string) {
	held := make(map[string]string, len(answers))
	for token, answer := range answers {
		held[token] = answer
	}

	m.challengeMu.Lock()
	defer m.challengeMu.Unlock()
	m.challenges = held
}

func (m *Manager) challenge(path string) (string, bool) {
	m.challengeMu.Lock()
	defer m.challengeMu.Unlock()

	answer, held := m.challenges[strings.TrimPrefix(path, challengePrefix)]
	return answer, held
}
