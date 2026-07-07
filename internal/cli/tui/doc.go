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
// Injection into a viewed agent is S2: the overlay renders no input line and
// has no send action yet.
package tui
