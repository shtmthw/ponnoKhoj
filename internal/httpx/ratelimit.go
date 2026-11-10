package httpx

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter is an in-memory, per-key token bucket limiter. State is per
// process: with N web replicas the effective limit is up to N times higher,
// which is acceptable for abuse damping (see ADR 0007).
type RateLimiter struct {
	perMinute int
	burst     int
	now       func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter allows perMinute events per key per minute, with a burst of
// half of that (at least 1), so a person clicking through a few results is
// not throttled while a script is.
func NewRateLimiter(perMinute int) *RateLimiter {
	burst := max(perMinute/2, 1)
	return &RateLimiter{perMinute: perMinute, burst: burst, now: time.Now, buckets: map[string]*bucket{}}
}

// Allow reports whether an event for key may proceed now.
func (l *RateLimiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > time.Minute {
		// Drop buckets idle long enough to have refilled completely.
		for k, b := range l.buckets {
			if now.Sub(b.lastSeen) > 2*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(rate.Limit(float64(l.perMinute)/60.0), l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	return b.limiter.AllowN(now, 1)
}

// Middleware limits requests per client IP. onLimit renders the 429 response
// (HTML or JSON depending on the surface).
func (l *RateLimiter) Middleware(clientIP func(*http.Request) string, onLimit http.HandlerFunc) Middleware {
	retryAfter := strconv.Itoa(max(60/l.perMinute, 1))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(clientIP(r)) {
				w.Header().Set("Retry-After", retryAfter)
				onLimit(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
