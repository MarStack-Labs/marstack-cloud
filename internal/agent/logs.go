package agent

import (
	"bytes"
	"context"
	"io"
	"os"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

const (
	maxShipBytes    = 64 * 1024
	maxShipLines    = 500
	maxHeldLineSize = 8 * 1024
)

func (a *Agent) shipLogs(ctx context.Context, assigned []instanceView) {
	for _, in := range assigned {
		runtime, known := a.runtimeFor(in.Isolation)
		if !known {
			continue
		}

		logged, able := runtime.(workload.Logged)
		if !able {
			continue
		}

		path, has := logged.LogPath(in.ID)
		if !has {
			continue
		}

		lines, next, err := a.readSince(in.ID, path)
		if err != nil {
			a.log.Warn("could not read a workload log",
				"instance", in.ID, "path", path, "error", err)
			continue
		}
		if len(lines) == 0 {
			continue
		}

		if err := a.client.reportLogs(ctx, a.currentNodeID(), in.ID, lines); err != nil {
			a.log.Warn("could not ship a workload log", "instance", in.ID, "error", err)
			continue
		}
		a.keepOffset(in.ID, next)
	}
}

func (a *Agent) readSince(instanceID, path string) ([]string, int64, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}

	a.logsMu.Lock()
	offset := a.logOffsets[instanceID]
	a.logsMu.Unlock()

	if info.Size() < offset {
		offset = 0
	}
	if info.Size() == offset {
		return nil, offset, nil
	}

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}

	buffer := make([]byte, maxShipBytes)
	read, err := io.ReadFull(file, buffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, 0, err
	}
	buffer = buffer[:read]

	lines, consumed := splitLines(buffer, int64(read) < info.Size()-offset)
	return lines, offset + int64(consumed), nil
}

func splitLines(buffer []byte, more bool) ([]string, int) {
	if !bytes.ContainsRune(buffer, '\n') {
		if more && len(buffer) >= maxHeldLineSize {
			return []string{string(buffer)}, len(buffer)
		}
		return nil, 0
	}

	lines := make([]string, 0, 16)
	consumed := 0
	for consumed < len(buffer) && len(lines) < maxShipLines {
		next := bytes.IndexByte(buffer[consumed:], '\n')
		if next < 0 {
			break
		}
		raw := buffer[consumed : consumed+next]
		lines = append(lines, string(bytes.TrimRight(raw, "\r")))
		consumed += next + 1
	}
	return lines, consumed
}

func (a *Agent) keepOffset(instanceID string, offset int64) {
	a.logsMu.Lock()
	defer a.logsMu.Unlock()
	a.logOffsets[instanceID] = offset
}

func (a *Agent) forgetLogs(assigned []instanceView) {
	wanted := make(map[string]bool, len(assigned))
	for _, in := range assigned {
		wanted[in.ID] = true
	}

	a.logsMu.Lock()
	defer a.logsMu.Unlock()

	for id := range a.logOffsets {
		if !wanted[id] {
			delete(a.logOffsets, id)
		}
	}
}
