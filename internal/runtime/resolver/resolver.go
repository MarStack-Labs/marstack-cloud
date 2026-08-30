package resolver

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	Port            = 53
	maxMessage      = 1232
	upstreamTimeout = 3 * time.Second
	zoneSuffix      = ".internal"
)

type Resolver struct {
	log *slog.Logger

	mu        sync.RWMutex
	records   map[string][]netip.Addr
	upstreams []string
	listeners map[string]*net.UDPConn
}

func New(log *slog.Logger) *Resolver {
	return &Resolver{
		log:       log,
		records:   map[string][]netip.Addr{},
		listeners: map[string]*net.UDPConn{},
		upstreams: discoverUpstreams(),
	}
}

func (r *Resolver) Update(records map[string][]string) {
	parsed := make(map[string][]netip.Addr, len(records))
	for name, addresses := range records {
		key := strings.ToLower(strings.TrimSuffix(name, "."))

		for _, ip := range addresses {
			addr, err := netip.ParseAddr(ip)
			if err != nil || addr.Is4In6() {
				continue
			}
			parsed[key] = append(parsed[key], addr)
		}
	}

	r.mu.Lock()
	r.records = parsed
	r.mu.Unlock()
}

func pick(held []netip.Addr, qtype uint16) (netip.Addr, bool) {
	for _, addr := range held {
		if qtype == typeA && addr.Is4() {
			return addr, true
		}
		if qtype == typeAAAA && addr.Is6() {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

func (r *Resolver) Listen(ctx context.Context, address string) error {
	r.mu.Lock()
	if _, running := r.listeners[address]; running {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	addr := net.JoinHostPort(address, fmt.Sprint(Port))
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	udp, ok := conn.(*net.UDPConn)
	if !ok {
		conn.Close()
		return fmt.Errorf("listener on %s is not udp", addr)
	}

	r.mu.Lock()
	r.listeners[address] = udp
	r.mu.Unlock()

	r.log.Info("resolver listening", "addr", addr, "upstreams", r.upstreams)

	go r.serve(ctx, address, udp)
	return nil
}

func (r *Resolver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for address, conn := range r.listeners {
		conn.Close()
		delete(r.listeners, address)
	}
}

func (r *Resolver) serve(ctx context.Context, address string, conn *net.UDPConn) {
	defer func() {
		conn.Close()
		r.mu.Lock()
		delete(r.listeners, address)
		r.mu.Unlock()
	}()

	buf := make([]byte, maxMessage)
	for {
		if ctx.Err() != nil {
			return
		}

		read, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				r.log.Warn("resolver read failed", "error", err)
			}
			return
		}

		query := make([]byte, read)
		copy(query, buf[:read])

		go r.handle(conn, from, query)
	}
}

func (r *Resolver) handle(conn *net.UDPConn, from *net.UDPAddr, query []byte) {
	q, err := parseQuestion(query)
	if err != nil {
		return
	}

	if !strings.HasSuffix(q.name, zoneSuffix) {
		r.forward(conn, from, query)
		return
	}

	r.mu.RLock()
	held, known := r.records[q.name]
	r.mu.RUnlock()

	addr, servable := pick(held, q.qtype)

	switch {
	case !known:
		_, _ = conn.WriteToUDP(emptyAnswer(query, q, rcodeNameError), from)
	case servable:
		_, _ = conn.WriteToUDP(answer(query, q, addr), from)
	default:
		_, _ = conn.WriteToUDP(emptyAnswer(query, q, 0), from)
	}
}

func (r *Resolver) forward(conn *net.UDPConn, from *net.UDPAddr, query []byte) {
	r.mu.RLock()
	upstreams := r.upstreams
	r.mu.RUnlock()

	for _, upstream := range upstreams {
		reply, err := exchange(upstream, query)
		if err != nil {
			continue
		}
		_, _ = conn.WriteToUDP(reply, from)
		return
	}
}

func exchange(upstream string, query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(upstream, fmt.Sprint(Port)), upstreamTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(upstreamTimeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}

	buf := make([]byte, maxMessage)
	read, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:read], nil
}

func discoverUpstreams() []string {
	file, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	defer file.Close()

	servers := make([]string, 0, 3)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		addr, err := netip.ParseAddr(fields[1])
		if err != nil || !addr.Is4() || addr.IsLoopback() {
			continue
		}
		servers = append(servers, addr.String())
	}

	if len(servers) == 0 {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	return servers
}
