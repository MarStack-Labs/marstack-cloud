//go:build linux

package qemu

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const qmpTimeout = 5 * time.Second

type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

type qmpReply struct {
	Return json.RawMessage `json:"return"`
	Error  *qmpError       `json:"error"`
	Event  string          `json:"event"`
}

type qmpConn struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

func dialQMP(path string) (*qmpConn, error) {
	conn, err := net.DialTimeout("unix", path, qmpTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial the monitor: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(qmpTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set the monitor deadline: %w", err)
	}

	q := &qmpConn{conn: conn, scanner: bufio.NewScanner(conn)}
	q.scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	if !q.scanner.Scan() {
		conn.Close()
		return nil, fmt.Errorf("the monitor sent no greeting")
	}
	if _, err := q.run("qmp_capabilities", nil); err != nil {
		conn.Close()
		return nil, err
	}
	return q, nil
}

func (q *qmpConn) close() error {
	return q.conn.Close()
}

func (q *qmpConn) run(command string, arguments map[string]any) (json.RawMessage, error) {
	request := map[string]any{"execute": command}
	if arguments != nil {
		request["arguments"] = arguments
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", command, err)
	}
	if _, err := q.conn.Write(append(encoded, '\n')); err != nil {
		return nil, fmt.Errorf("send %s: %w", command, err)
	}

	for q.scanner.Scan() {
		var reply qmpReply
		if err := json.Unmarshal(q.scanner.Bytes(), &reply); err != nil {
			return nil, fmt.Errorf("decode the reply to %s: %w", command, err)
		}
		if reply.Event != "" {
			continue
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("%s: %s", command, reply.Error.Desc)
		}
		return reply.Return, nil
	}

	if err := q.scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the reply to %s: %w", command, err)
	}
	return nil, fmt.Errorf("the monitor closed while running %s", command)
}

func (q *qmpConn) attached() (map[string]bool, error) {
	raw, err := q.run("query-block", nil)
	if err != nil {
		return nil, err
	}

	var devices []struct {
		Device   string `json:"device"`
		Inserted struct {
			File string `json:"file"`
		} `json:"inserted"`
	}
	if err := json.Unmarshal(raw, &devices); err != nil {
		return nil, fmt.Errorf("decode the block list: %w", err)
	}

	present := make(map[string]bool, 2*len(devices))
	for _, d := range devices {
		if d.Device != "" {
			present[d.Device] = true
		}
		if d.Inserted.File != "" {
			present[d.Inserted.File] = true
		}
	}
	return present, nil
}
