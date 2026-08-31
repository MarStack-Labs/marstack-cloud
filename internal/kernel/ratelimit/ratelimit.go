package ratelimit

import (
	"sync"
	"time"
)

const (
	DefaultPerSecond = 50
	DefaultBurst     = 100

	DefaultProjectPerSecond = 200
	DefaultProjectBurst     = 400

	MaxTracked = 4096

	idle = 5 * time.Minute
)

type bucket struct {
	tokens float64
	seenAt time.Time
}

type Limiter struct {
	perSecond float64
	burst     float64
	now       func() time.Time

	mu       sync.Mutex
	buckets  map[string]*bucket
	shared   *bucket
	sweptAt  time.Time
	overflow bool
}

func New(perSecond, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if perSecond <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = perSecond
	}

	start := now()
	return &Limiter{
		perSecond: float64(perSecond),
		burst:     float64(burst),
		now:       now,
		buckets:   make(map[string]*bucket),
		shared:    &bucket{tokens: float64(burst), seenAt: start},
		sweptAt:   start,
	}
}

func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	b, known := l.buckets[key]
	switch {
	case known:
	case len(l.buckets) < MaxTracked:
		b = &bucket{tokens: l.burst, seenAt: now}
		l.buckets[key] = b
	default:
		l.overflow = true
		b = l.shared
	}

	b.tokens += now.Sub(b.seenAt).Seconds() * l.perSecond
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.seenAt = now

	if b.tokens < 1 {
		return false, time.Duration((1-b.tokens)/l.perSecond*float64(time.Second)) + time.Second
	}
	b.tokens--
	return true, 0
}

func (l *Limiter) Overflowed() bool {
	if l == nil {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.overflow
}

func (l *Limiter) Tracked() int {
	if l == nil {
		return 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.sweptAt) < idle {
		return
	}
	l.sweptAt = now

	for key, b := range l.buckets {
		if now.Sub(b.seenAt) >= idle {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < MaxTracked {
		l.overflow = false
	}
}
