package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Sessions is an in-memory admin session store with an idle timeout. A
// gateway restart logs the admin out, which is acceptable for one admin.
type Sessions struct {
	mu   sync.Mutex
	idle time.Duration
	m    map[string]session
	Now  func() time.Time
}

type session struct {
	actor    string
	lastSeen time.Time
}

func NewSessions(idle time.Duration) *Sessions {
	return &Sessions{idle: idle, m: map[string]session{}, Now: time.Now}
}

func (s *Sessions) Create(actor string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = session{actor: actor, lastSeen: s.Now()}
	return id, nil
}

func (s *Sessions) Lookup(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[id]
	now := s.Now()
	if !ok || now.Sub(sess.lastSeen) > s.idle {
		delete(s.m, id)
		return "", false
	}
	sess.lastSeen = now
	s.m[id] = sess
	return sess.actor, true
}

func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}
