package coord

import (
	"sync"
	"time"
)

// spoolCredit decides which delivered-record entries are NEW progress.
//
// Two rules, each closing one way to over-credit:
//
//   - FLOOR: an entry recorded before this coordinator's spool reader
//     started is history. Without the floor, the first sweep after a restart
//     would credit every delivery still inside the record's retention window
//     as fresh progress and forgive relaunch budgets nothing earned.
//   - LAST LISTING: an entry seen on an earlier sweep is not credited again.
//     The memory is that listing, REPLACED every sweep rather than grown, so
//     it is bounded by the record's own retention (spool.DeliveredRetention).
type spoolCredit struct {
	mu    sync.Mutex
	floor time.Time
	seen  map[string]map[string]bool
}

// start sets the floor. Called once, when the spool reader starts.
func (s *spoolCredit) start(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.floor = now
	s.seen = map[string]map[string]bool{}
}

// credit reports how many of role's delivered identities are new progress,
// and remembers this listing as role's last.
func (s *spoolCredit) credit(role string, ids map[string]time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.seen[role]
	fresh := 0
	next := make(map[string]bool, len(ids))
	for id, at := range ids {
		next[id] = true
		if prev[id] || at.Before(s.floor) {
			continue
		}
		fresh++
	}
	if s.seen == nil {
		s.seen = map[string]map[string]bool{}
	}
	s.seen[role] = next
	return fresh
}
