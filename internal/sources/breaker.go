package sources

import (
	"sync"
	"time"
)

// Breaker is a small circuit breaker. After threshold consecutive failures
// it opens and rejects calls for cooldown; then it lets a single probe
// through (half-open). A successful probe closes it, a failed one re-opens.
type Breaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu       sync.Mutex
	failures int
	openedAt time.Time
	open     bool
	probing  bool
}

// NewBreaker builds a breaker.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

// Allow reports whether a call may proceed. Every allowed call must be
// followed by exactly one Record.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if b.probing || b.now().Sub(b.openedAt) < b.cooldown {
		return false
	}
	b.probing = true
	return true
}

// Record reports the outcome of an allowed call.
func (b *Breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.failures, b.open, b.probing = 0, false, false
		return
	}
	b.failures++
	if b.probing || b.failures >= b.threshold {
		b.open, b.probing, b.openedAt = true, false, b.now()
	}
}

// Open reports whether the breaker is currently rejecting calls.
func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}
