package coord

import (
	"sync"
	"time"
)

// spoolCredit decides which delivered-record entries are NEW progress.
//
// Its memory, per role, is the last listing of that role's delivered record:
//
//   - SEEDED AT START, without crediting, for every harp the coordinator has
//     a run record for. Without the seed, the first sweep after a restart
//     would credit every delivery still inside the record's retention window
//     as fresh progress and forgive relaunch budgets nothing earned. The seed
//     is the record itself rather than a start time: an entry's time is its
//     file's mtime, stamped from the kernel's coarse clock, and a delivery
//     made just after a start-time floor can carry an mtime before it.
//   - REPLACED every sweep rather than grown, so it is bounded by the
//     record's own retention (spool.DeliveredRetention).
//
// A role first seen after start has no seed: everything in its record was
// delivered during this coordinator's life.
type spoolCredit struct {
	mu   sync.Mutex
	seen map[string]map[string]bool
}

// seed remembers role's current listing as already credited.
func (s *spoolCredit) seed(role string, ids map[string]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]map[string]bool{}
	}
	s.seen[role] = keysOf(ids)
}

// credit reports how many of role's delivered identities are new progress,
// and remembers this listing as role's last.
func (s *spoolCredit) credit(role string, ids map[string]time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]map[string]bool{}
	}
	prev := s.seen[role]
	fresh := 0
	for id := range ids {
		if !prev[id] {
			fresh++
		}
	}
	s.seen[role] = keysOf(ids)
	return fresh
}

func keysOf(ids map[string]time.Time) map[string]bool {
	out := make(map[string]bool, len(ids))
	for id := range ids {
		out[id] = true
	}
	return out
}
