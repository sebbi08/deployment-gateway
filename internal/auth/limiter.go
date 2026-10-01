package auth

import (
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
}

func NewLimiter(max int, window, block time.Duration) *Limiter {
	return &Limiter{max: max, window: window, block: block,
		fails: map[string][]time.Time{}, blocked: map[string]time.Time{}, Now: time.Now}
}

func (l *Limiter) Blocked(key string) bool {
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

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
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
		return
	}
	l.fails[key] = kept
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
