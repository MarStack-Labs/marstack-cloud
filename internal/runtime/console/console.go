package console

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRoot = "/var/lib/marstack"

	UpstreamName = "serial.sock"
	AttachName   = "console.sock"
	LogName      = "console.log"
	LoginName    = "console-login"

	dialEvery  = 50 * time.Millisecond
	bufferSize = 4096
)

var dialWindow = 5 * time.Second

var ErrNoConsole = errors.New("no console on this node for that instance")

type Hub struct {
	log      *slog.Logger
	upstream net.Conn
	file     *os.File
	listener net.Listener

	mu      sync.Mutex
	clients map[net.Conn]struct{}
	closed  bool
}

func Attach(dir string, log *slog.Logger) (*Hub, error) {
	upstream, err := dial(filepath.Join(dir, UpstreamName))
	if err != nil {
		return nil, err
	}

	file, err := openLog(dir)
	if err != nil {
		upstream.Close()
		return nil, fmt.Errorf("open console log: %w", err)
	}

	socket := filepath.Join(dir, AttachName)
	_ = os.Remove(socket)

	listener, err := net.Listen("unix", socket)
	if err != nil {
		upstream.Close()
		file.Close()
		return nil, fmt.Errorf("listen on the attach socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		upstream.Close()
		file.Close()
		listener.Close()
		return nil, fmt.Errorf("restrict the attach socket: %w", err)
	}

	hub := &Hub{
		log:      log,
		upstream: upstream,
		file:     file,
		listener: listener,
		clients:  map[net.Conn]struct{}{},
	}

	go hub.pump()
	go hub.accept()

	return hub, nil
}

func openLog(dir string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	return root.OpenFile(LogName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func dial(path string) (net.Conn, error) {
	deadline := time.Now().Add(dialWindow)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect to the serial socket: %w", err)
		}
		time.Sleep(dialEvery)
	}
}

func (h *Hub) pump() {
	buffer := make([]byte, bufferSize)
	for {
		read, err := h.upstream.Read(buffer)
		if read > 0 {
			if _, writeErr := h.file.Write(buffer[:read]); writeErr != nil {
				h.log.Warn("cannot write the console log", "error", writeErr)
			}
			h.broadcast(buffer[:read])
		}
		if err != nil {
			return
		}
	}
}

func (h *Hub) broadcast(payload []byte) {
	h.mu.Lock()
	targets := make([]net.Conn, 0, len(h.clients))
	for client := range h.clients {
		targets = append(targets, client)
	}
	h.mu.Unlock()

	for _, client := range targets {
		if _, err := client.Write(payload); err != nil {
			h.drop(client)
		}
	}
}

func (h *Hub) accept() {
	for {
		client, err := h.listener.Accept()
		if err != nil {
			return
		}

		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			client.Close()
			return
		}
		h.clients[client] = struct{}{}
		h.mu.Unlock()

		go func() {
			_, _ = io.Copy(h.upstream, client)
			h.drop(client)
		}()
	}
}

func (h *Hub) drop(client net.Conn) {
	h.mu.Lock()
	_, known := h.clients[client]
	delete(h.clients, client)
	h.mu.Unlock()

	if known {
		client.Close()
	}
}

func (h *Hub) attached() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	clients := make([]net.Conn, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.clients = map[net.Conn]struct{}{}
	h.mu.Unlock()

	h.listener.Close()
	h.upstream.Close()
	for _, client := range clients {
		client.Close()
	}
	return h.file.Close()
}

func Login(socket string) string {
	root, err := os.OpenRoot(filepath.Dir(socket))
	if err != nil {
		return ""
	}
	defer root.Close()

	file, err := root.Open(LoginName)
	if err != nil {
		return ""
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, 256))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func Find(root, instanceID string) (string, error) {
	if root == "" {
		root = DefaultRoot
	}

	matches, err := filepath.Glob(filepath.Join(root, "*", instanceID, AttachName))
	if err != nil {
		return "", fmt.Errorf("look for a console socket: %w", err)
	}
	if len(matches) == 0 {
		return "", ErrNoConsole
	}
	return matches[0], nil
}
