// Package pidalive provides ONE shared liveness probe for a bare pid,
// consolidating what used to be three near-identical copies
// (agentcoord/coord.PidAlive, internal/operations' test-only pidAlive, and
// internal/lm/isolation's startup-reaper pidAlive) into a single leaf package
// with no internal dependencies — safe for any package to import without
// creating a cycle. Probe itself is platform-specific (pidalive_unix.go,
// pidalive_windows.go); State and MaybeAlive here are the shared,
// platform-agnostic verdict type both implementations return.
package pidalive

// State is Probe's tri-state verdict for a pid liveness check (U114-F01). A
// bare bool return forced every caller to collapse "I could not tell" into a
// confident answer, and every caller collapsed it the same way: false
// ("dead") — the destructive direction for a reaping/claiming decision. State
// makes "I don't know" representable so a caller can choose its own honest
// policy instead of inheriting one silently. See MaybeAlive for the shared
// conservative policy every current caller in this repo wants.
type State int

const (
	// Dead means the probe confirmed the pid names no live process.
	Dead State = iota
	// Alive means the probe confirmed the pid names a live process (whether
	// or not this process can actually signal/open it).
	Alive
	// Unsure means the probe could not tell — an unexpected outcome outside
	// the well-documented cases for this platform's probe mechanism.
	Unsure
)

// MaybeAlive reports whether pid COULD be alive: true for both Alive and
// Unsure, false only for a confirmed Dead. Every production caller of Probe
// in this repo makes a refuse/skip decision on this question (claim a state
// dir, reap a worktree, start a second coordinator) where wrongly treating
// an unsure pid as dead risks corrupting shared state, destroying a live
// worktree, or spawning a rogue duplicate — all worse than a needlessly
// conservative skip. Use this instead of comparing `== Alive` directly
// unless a caller has a specific, considered reason to require certainty.
func (s State) MaybeAlive() bool { return s != Dead }
