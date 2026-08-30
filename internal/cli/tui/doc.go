// Package tui is the prefix-engaged agent-observation overlay for interactive
// `ctxloom run` sessions (agent-io observation plan §4/§4a, slice S1b): a
// bubbletea model presenting a lineage-indented roster beside the selected
// harp's observation feed, with follow-mode scrollback, expandable tool
// detail, transcript export, and OSC 52 copy.
//
// The package implements termui.Overlay and is the ONLY place the TUI
// framework is linked — the interceptor and surround bar (internal/termui)
// stay framework-free on the hot path. All data arrives through the Sources
// seams (the operations feed resolver, the session index, the agent-bus
// roster), injected by the CLI wiring and faked in tests, so the model is
// hermetically testable through Update without a terminal.
//
// Injection into a viewed agent is implemented: an inject input line opens
// on demand (openInject), owns the keymap while open (updateInjectKey), and
// sends through Sources.Inject (injectCmd).
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
package tui
