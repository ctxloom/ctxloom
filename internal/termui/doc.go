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
package termui
