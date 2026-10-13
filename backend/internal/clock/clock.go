// Package clock abstracts "now" so business timing (T+3, 48h, 7d) can be
// tested and demoed by advancing a dev clock instead of waiting.
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type Real struct{}

func (Real) Now() time.Time { return time.Now().UTC() }

// Offset is a real clock shifted by an adjustable offset (dev mode only).
type Offset struct {
	mu     sync.RWMutex
	offset time.Duration
}

func (o *Offset) Now() time.Time {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return time.Now().UTC().Add(o.offset)
}

func (o *Offset) Advance(d time.Duration) {
	o.mu.Lock()
	o.offset += d
	o.mu.Unlock()
}

func (o *Offset) Offset() time.Duration {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.offset
}

// Fixed is a manually driven clock for tests.
type Fixed struct {
	mu sync.Mutex
	t  time.Time
}

func NewFixed(t time.Time) *Fixed { return &Fixed{t: t.UTC()} }

func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func Ms(t time.Time) int64 { return t.UnixMilli() }

func FromMs(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
