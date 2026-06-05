package harness

import "sort"

// Minimal Claude-shaped settings model. The owner predicate is engine-agnostic;
// only this shape (and the marshaller, out of scope here) differs per engine
// adapter. statusLine is a single owned-or-not slot, analogous and omitted.
type HookEntry struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
}

type HookGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []HookEntry `json:"hooks"`
}

type Settings struct {
	Hooks map[string][]HookGroup `json:"hooks,omitempty"`
}

// Commands lists every hook command currently in s (sorted), regardless of
// owner.
func Commands(s *Settings) []string {
	var out []string
	for _, groups := range s.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				out = append(out, h.Command)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Reconcile makes s reflect exactly owner o's desired hook entries: every entry
// o owns is removed first, then desired is appended; entries o does NOT own
// (the user's and other tools') are left untouched, and emptied groups/events
// are pruned.
//
// Idempotent (re-running converges to one entry) and migration-safe (an owner's
// old, verb-drifted entries match by executable token and are replaced, not
// orphaned) — both fall out of exec-token identity, no bookkeeping needed.
func Reconcile(s *Settings, o Owner, desired map[string][]HookGroup) {
	if s.Hooks == nil {
		s.Hooks = make(map[string][]HookGroup)
	}

	events := make([]string, 0, len(s.Hooks))
	for e := range s.Hooks {
		events = append(events, e)
	}
	for _, event := range events {
		var kept []HookGroup
		for _, g := range s.Hooks[event] {
			var hooks []HookEntry
			for _, h := range g.Hooks {
				if !o.Owns(h.Command) {
					hooks = append(hooks, h)
				}
			}
			if len(hooks) > 0 {
				g.Hooks = hooks
				kept = append(kept, g)
			}
		}
		if len(kept) > 0 {
			s.Hooks[event] = kept
		} else {
			delete(s.Hooks, event)
		}
	}

	devents := make([]string, 0, len(desired))
	for e := range desired {
		devents = append(devents, e)
	}
	sort.Strings(devents)
	for _, event := range devents {
		s.Hooks[event] = append(s.Hooks[event], desired[event]...)
	}
}
