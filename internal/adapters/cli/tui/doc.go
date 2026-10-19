// Package tui is the prefix-engaged agent-observation overlay for interactive
// `ctxloom run` sessions (agent-io observation plan §4/§4a, slice S1b): a
// bubbletea model presenting a lineage-indented roster beside the selected
// harp's observation feed, with follow-mode scrollback and expandable tool
// detail.
//
// The package implements termui.Overlay and is the ONLY place the TUI
// framework is linked — the interceptor and surround bar (internal/adapters/termui)
// stay framework-free on the hot path. All data arrives through the frontend
// port (Sources.Port, internal/core/frontend) — the session index, the
// coordinator's runs, one harp's feed — injected by the CLI wiring and faked
// in tests (frontendfake), so the model is hermetically testable through
// Update without a terminal; it never holds a coordinator.
//
// Control of a viewed agent rides one seam, the port's Control: openControl
// starts a verb (controlKeys), a verb that carries text opens an input line
// that owns the keymap while open (updateComposeKey), and sendControl makes
// the round trip.
//
// Approvals ride the port's Approvals, the root's coord.ApprovalSource: the
// approvals view lists the parked requests and is the only place one is
// answered. termui summons it as the full-screen modal (OverlayStart.Summoned)
// or the human opens it from the roster view; either way its keymap is the
// modal's fourth focus lock (see approvalsModel), and every child-supplied
// string it shows passes displaysafe.Text.
//
// The package also holds the split view that replaces the overlay and the
// terminal layer under it (Split, started by StartSplit): the agent tree
// beside the engine's terminal, drawn by one owner through one emulator
// (internal/adapters/termemu), with a child shown as its feed above an input
// line. It reuses the roster, the feed renderer, the control verbs and the
// approvals model, and keeps the modal's focus locks (PresentPolicy) for a
// summoned approval. It is not yet what `ctxloom run` starts.
package tui
