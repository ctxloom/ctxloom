// Package termui is the frontend terminal layer for interactive `ctxloom run`
// sessions: a prefix-key interceptor on the raw stdin stream, a persistent
// surround bar on reserved bottom rows, an engine-output hold gate, and the
// resize translation that reserves the bar rows (agent-io observation plan
// §4/§4a, slice S1b).
//
// Everything here is deliberately framework-free raw ANSI: the interceptor and
// the output gate sit on the interactive hot path (every keystroke, every
// engine output chunk) and must add no per-byte allocations. The bubbletea
// overlay lives behind the Overlay interface (internal/cli/tui) so this
// package never links a TUI framework.
//
// All tty writes are serialized under one lock (the controller's ttyMu), and
// child bytes pass through the gate's vtGuard, which defends the surround's
// reserved row against child scroll-region clobbers and pins bar repaints to
// sequence-safe stream boundaries (see vtguard.go).
//
// The layer only ever WRAPS the frontend's existing seams — the stdin reader,
// the stdout writer, and the resize channel handed to the plugin client's Run
// — and a failure anywhere in it degrades to a plain terminal with a streamed
// warning; it never kills the engine session.
//
// # This package is a PARALLEL system, deliberately, and it is on a clock
//
// ctxloom is growing a second, unrelated way to observe an agent: the ACP
// server (internal/acpagent) driving an editor or a deliberately ultra-simple
// terminal rendering of the LLM UI. That is not drift and it is not an
// oversight — it was chosen by the maintainer on 2026-08-30, with this package
// left in place ON PURPOSE while the replacement is built.
//
// THE EXIT CONDITION IS PART OF THE DECISION: this coexistence lasts UNTIL THE
// NEW SYSTEM IS PROVEN. At that point this stack is removed and the "no
// multiplexer available" gap is answered cleanly, on its own terms, rather
// than by leaving this here forever as an accidental fallback. Read that as
// two obligations, not one: do not delete this before the replacement is
// proven, and do not treat its survival as settled once it is.
//
// So: do NOT "fix" the duplication by collapsing the two, and do NOT extend
// this package to close gaps in the new one — that trades a bounded
// coexistence for a permanent one. The project's standing rule is
// delete-over-deprecate with no parallel implementations; this is a stated
// exception to it, which is exactly why it is written down here instead of
// left to be inferred.
//
// Rulings of record, with the accepted costs:
// ~/.ctxloom/sessions/gusty-bleak-tweak/persist/tmux-acp-preflight.plan.md
package termui
