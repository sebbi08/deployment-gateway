package service

import "time"

// Test hooks for the background secret applier.

func (s *Service) EnqueueApply(ws string, appIDs []int64) { s.enqueueApply(ws, appIDs) }

// ApplyIdle reports whether no apply job is queued or running.
func (s *Service) ApplyIdle() bool {
	q := &s.applies
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending) == 0 && len(q.running) == 0
}

func (s *Service) SetApplyPoll(d time.Duration) { s.applyPoll = d }
