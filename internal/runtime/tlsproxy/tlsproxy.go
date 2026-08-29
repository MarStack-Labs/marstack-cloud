package tlsproxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const dialTimeout = 5 * time.Second

type Endpoint struct {
	ID          string
	ListenPort  int
	TargetPort  int
	Certificate string
	PrivateKey  string
	Targets     []string
}

func (e Endpoint) fingerprint() string {
	sum := sha256.Sum256([]byte(strconv.Itoa(e.ListenPort) + "\x00" +
		e.Certificate + "\x00" + e.PrivateKey))
	return hex.EncodeToString(sum[:])
}

type listener struct {
	fingerprint string
	targets     atomic.Pointer[[]string]
	targetPort  int
	next        atomic.Uint64
	net         net.Listener
	stop        context.CancelFunc
	done        chan struct{}
}

type Manager struct {
	log *slog.Logger

	mu      sync.Mutex
	running map[string]*listener
}

func New(log *slog.Logger) *Manager {
	return &Manager{log: log, running: map[string]*listener{}}
}

func (m *Manager) Apply(ctx context.Context, wanted []Endpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	keep := make(map[string]bool, len(wanted))
	var failed error

	for _, e := range wanted {
		keep[e.ID] = true

		existing, up := m.running[e.ID]
		if up && existing.fingerprint == e.fingerprint() {
			targets := append([]string(nil), e.Targets...)
			existing.targets.Store(&targets)
			continue
		}
		if up {
			m.shutdown(e.ID, existing)
		}

		started, err := m.start(ctx, e)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		m.running[e.ID] = started
	}

	for id, l := range m.running {
		if !keep[id] {
			m.shutdown(id, l)
		}
	}
	return failed
}

func (m *Manager) fingerprintOf(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if l, up := m.running[id]; up {
		return l.fingerprint
	}
	return ""
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, l := range m.running {
		m.shutdown(id, l)
	}
}

func (m *Manager) shutdown(id string, l *listener) {
	l.stop()
	l.net.Close()
	<-l.done
	delete(m.running, id)
	m.log.Info("tls listener stopped", "balancer", id)
}

func (m *Manager) start(ctx context.Context, e Endpoint) (*listener, error) {
	pair, err := tls.X509KeyPair([]byte(e.Certificate), []byte(e.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("balancer %s: load the certificate: %w", e.ID, err)
	}

	raw, err := net.Listen("tcp", ":"+strconv.Itoa(e.ListenPort))
	if err != nil {
		return nil, fmt.Errorf("balancer %s: listen on %d: %w", e.ID, e.ListenPort, err)
	}

	guarded := tls.NewListener(raw, &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
	})

	inner, stop := context.WithCancel(ctx)
	l := &listener{
		fingerprint: e.fingerprint(),
		targetPort:  e.TargetPort,
		net:         guarded,
		stop:        stop,
		done:        make(chan struct{}),
	}
	targets := append([]string(nil), e.Targets...)
	l.targets.Store(&targets)

	go m.serve(inner, e.ID, l)

	m.log.Info("tls listener started",
		"balancer", e.ID, "port", e.ListenPort, "targets", len(targets))
	return l, nil
}

func (m *Manager) serve(ctx context.Context, id string, l *listener) {
	defer close(l.done)

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		conn, err := l.net.Accept()
		if err != nil {
			if ctx.Err() == nil {
				m.log.Warn("tls listener stopped accepting", "balancer", id, "error", err)
			}
			return
		}

		target, ok := l.pick()
		if !ok {
			conn.Close()
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			m.relay(conn, net.JoinHostPort(target, strconv.Itoa(l.targetPort)), id)
		}()
	}
}

func (l *listener) pick() (string, bool) {
	targets := l.targets.Load()
	if targets == nil || len(*targets) == 0 {
		return "", false
	}

	list := *targets
	index := l.next.Add(1) - 1
	return list[index%uint64(len(list))], true
}

func (m *Manager) relay(front net.Conn, address, id string) {
	defer front.Close()

	back, err := net.DialTimeout("tcp", address, dialTimeout)
	if err != nil {
		m.log.Warn("tls balancer could not reach a backend",
			"balancer", id, "backend", address, "error", err)
		return
	}
	defer back.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(back, front)
		if half, ok := back.(*net.TCPConn); ok {
			half.CloseWrite()
		}
	}()

	io.Copy(front, back)
	if half, ok := front.(*tls.Conn); ok {
		half.CloseWrite()
	}
	<-done
}
