package console

import (
	"log/slog"
	"sync"
)

type Keeper struct {
	log *slog.Logger

	mu        sync.Mutex
	hubs      map[string]*Hub
	attaching map[string]bool
	warned    map[string]bool
}

func NewKeeper(log *slog.Logger) *Keeper {
	return &Keeper{
		log:       log,
		hubs:      map[string]*Hub{},
		attaching: map[string]bool{},
		warned:    map[string]bool{},
	}
}

func (k *Keeper) Ensure(dir, instanceID string) {
	k.mu.Lock()
	_, live := k.hubs[instanceID]
	if live || k.attaching[instanceID] {
		k.mu.Unlock()
		return
	}
	k.attaching[instanceID] = true
	k.mu.Unlock()

	go k.attach(dir, instanceID)
}

func (k *Keeper) attach(dir, instanceID string) {
	hub, err := Attach(dir, k.log)

	k.mu.Lock()
	delete(k.attaching, instanceID)
	if err == nil {
		k.hubs[instanceID] = hub
		k.warned[instanceID] = false
	}
	quiet := k.warned[instanceID]
	if err != nil {
		k.warned[instanceID] = true
	}
	k.mu.Unlock()

	if err != nil && !quiet {
		k.log.Warn("cannot attach to the console of a running workload",
			"instance", instanceID, "error", err)
	}
}

func (k *Keeper) Release(instanceID string) {
	k.mu.Lock()
	hub, known := k.hubs[instanceID]
	delete(k.hubs, instanceID)
	delete(k.warned, instanceID)
	k.mu.Unlock()

	if known {
		hub.Close()
	}
}

func (k *Keeper) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.hubs)
}
