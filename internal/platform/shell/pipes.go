package shell

import (
	"io"
	"sync"
)

type pipes struct {
	toNode     *io.PipeReader
	toNodeIn   *io.PipeWriter
	toClient   *io.PipeReader
	toClientIn *io.PipeWriter

	once sync.Once
}

func newPipes() *pipes {
	nodeReader, nodeWriter := io.Pipe()
	clientReader, clientWriter := io.Pipe()

	return &pipes{
		toNode:     nodeReader,
		toNodeIn:   nodeWriter,
		toClient:   clientReader,
		toClientIn: clientWriter,
	}
}

func (p *pipes) close(reason error) {
	p.once.Do(func() {
		p.toNodeIn.CloseWithError(reason)
		p.toClientIn.CloseWithError(reason)
		p.toNode.CloseWithError(reason)
		p.toClient.CloseWithError(reason)
	})
}

type relay struct {
	mu   sync.Mutex
	held map[string]*pipes
}

func newRelay() *relay {
	return &relay{held: map[string]*pipes{}}
}

func (r *relay) open(id string) *pipes {
	r.mu.Lock()
	defer r.mu.Unlock()

	if held, found := r.held[id]; found {
		return held
	}

	held := newPipes()
	r.held[id] = held
	return held
}

func (r *relay) find(id string) (*pipes, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	held, found := r.held[id]
	return held, found
}

func (r *relay) shut(id string, reason error) {
	r.mu.Lock()
	held, found := r.held[id]
	delete(r.held, id)
	r.mu.Unlock()

	if found {
		held.close(reason)
	}
}
