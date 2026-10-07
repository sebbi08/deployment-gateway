package auth

import (
	"net/netip"
	"sync"
	"time"
)

// Limiter blocks a key (client IP) after max failures within window.
type Limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	block   time.Duration
	fails   map[string][]time.Time
	blocked map[string]time.Time
	Now     func() time.Time
	// lastSweep is when expired blocks were last removed; sweeps run at
	// most once per window and only once the map is large.
	lastSweep time.Time
}

// limiterKey keys IPv6 clients by their /64, so rotating addresses inside
// one prefix neither escapes the limit nor grows the maps.
func limiterKey(key string) string {
	a, err := netip.ParseAddr(key)
	if err != nil || a.Is4() || a.Is4In6() {
		return key
	}
	p, _ := a.Prefix(64)
	return p.String()
}

func NewLimiter(max int, window, block time.Duration) *Limiter {
	return &Limiter{max: max, window: window, block: block,
		fails: map[string][]time.Time{}, blocked: map[string]time.Time{}, Now: time.Now}
}

func (l *Limiter) Blocked(key string) bool {
	key = limiterKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.blocked[key]
	if !ok {
		return false
	}
	if l.Now().Before(until) {
		return true
	}
	delete(l.blocked, key)
	return false
}

// Fail records a failure and reports whether it started a block.
func (l *Limiter) Fail(key string) bool {
	key = limiterKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	if len(l.blocked) > 1024 && now.Sub(l.lastSweep) >= l.window {
		for k, until := range l.blocked {
			if !now.Before(until) {
				delete(l.blocked, k)
			}
		}
		l.lastSweep = now
	}
	if len(l.fails) > 10000 {
		l.fails = map[string][]time.Time{}
	}
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	if len(kept) >= l.max {
		l.blocked[key] = now.Add(l.block)
		delete(l.fails, key)
		return true
	}
	l.fails[key] = kept
	return false
}

func (l *Limiter) Reset(key string) {
	key = limiterKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

// BlockedCount returns the number of block entries (including expired ones).
func (l *Limiter) BlockedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.blocked)
}
