// Package tui is the prefix-engaged agent-observation overlay for interactive
// `ctxloom run` sessions (agent-io observation plan §4/§4a, slice S1b): a
// bubbletea model presenting a lineage-indented roster beside the selected
// harp's observation feed, with follow-mode scrollback and expandable tool
// detail.
//
// The package implements termui.Overlay and is the ONLY place the TUI
// framework is linked — the interceptor and surround bar (internal/adapters/termui)
// stay framework-free on the hot path. All data arrives through the Sources
// seams (the operations feed resolver, the session index, the agent-bus
// roster), injected by the CLI wiring and faked in tests, so the model is
// hermetically testable through Update without a terminal.
//
// Control of a viewed agent rides one seam, Sources.Control: openControl
// starts a verb (controlKeys), a verb that carries text opens an input line
// that owns the keymap while open (updateComposeKey), and sendControl makes
// the round trip.
package tui
