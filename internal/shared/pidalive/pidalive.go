// Package pidalive answers ONE question about a BARE PID: is some process
// alive at this number right now. It cannot answer "is the process I meant
// still alive", and callers must not read it that way. A pid is not a stable
// identifier — the kernel recycles numbers within kernel.pid_max — so a pid
// persisted to disk and probed later can name an unrelated process that has
// merely inherited the number, and Probe will confidently report Alive.
//
// Closing that window needs an identity token taken while the process is known
// to be the right one (a pidfd, or a start-time stamp read from the process
// table) and persisted alongside the pid; internal/lm/grpc's procHandle is the
// worked example, pinning identity with a pidfd before it reads /proc. Every
// caller here instead reads a pid out of a file written by an earlier process
// — an MCP discovery marker, a worktree owner file, a state-dir lock — and has
// no such token, so the protection would have to be added to those three
// on-disk formats, not here. Probe takes an int and nothing else precisely
// because it has no way to tell the difference.
//
// The exposure is bounded by MaybeAlive's direction: reuse makes a dead
// owner read as live, so the failure is a skipped reap or a refused claim,
// never a stranger's worktree deleted or a shared journal.
//
// It provides that probe once for the whole repo, consolidating what used to
// be three near-identical copies
// (agentcoord/coord.PidAlive, internal/operations' test-only pidAlive, and
// internal/lm/isolation's startup-reaper pidAlive) into a single leaf package
// with no internal dependencies — safe for any package to import without
// creating a cycle. Probe itself is platform-specific (pidalive_unix.go,
// pidalive_windows.go); State and MaybeAlive here are the shared,
// platform-agnostic verdict type both implementations return.
package pidalive

// pidNamesOneProcess reports whether pid can name a single process at all.
// Zero and the negatives cannot: kill(2) gives them entirely different
// meanings — 0 is "every process in the caller's own group", -1 is "every
// process this user may signal", and any other negative is the process GROUP
// with that absolute value. An unguarded Probe(-5) therefore probes group 5
// and answers Alive for something that is not the target at all, and Probe(0)
// answers about the caller itself, which is always Alive.
//
// Both platform probes reject these before probing so the two give the same
// answer to the same nonsense, rather than Unix quietly reinterpreting the
// argument while Windows rejects it. The verdict is Unsure, not Dead: the
// honest reading of an out-of-range pid is "I cannot tell you anything about
// this", and Unsure is the direction that makes a caller skip rather than
// reap. Every current caller already refuses a non-positive pid before
// calling; this makes the contract hold without relying on that.
func pidNamesOneProcess(pid int) bool { return pid > 0 }

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
