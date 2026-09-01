// Package tmuxhost hosts processes in tmux windows on a dedicated tmux server.
//
// A caller hands it a Spec — command, args, cwd, env — and gets back a
// TerminalID naming a real pty in its own tmux window. From that id it can read
// what the process has written (Output), block until it ends (Wait), take it
// down (Kill), and give the window and its files back (Release). PaneHost sits
// on top of the same machinery for the long-lived case: a pane an operator can
// attach to and, where it has been MEASURED to be safe, inject text into.
//
// # Why this is its own package
//
// It was born inside internal/acp, because ACP's terminal/* RPCs were its first
// caller. That was an accident of chronology, not a property of the capability:
// hosting a process on a pty in tmux has nothing to do with the Agent Client
// Protocol, and the pane path — `ctxloom attach`, injection — has no ACP in it
// at all. Left where it was, the whole thing would have been deleted along with
// internal/acp. Hence the ruling this package exists to satisfy: the tmux
// shouldn't be bound to acp.
//
// The name follows from that. It names the CAPABILITY — terminals and panes
// hosted in tmux — rather than any consumer, so that no future reader has to
// work out whether "acp" in the path still means anything.
//
// # The SDK line
//
// This package MUST NOT import github.com/coder/acp-go-sdk, and that is a
// standing invariant rather than a present-tense fact about the imports. Every
// type crossing its boundary is local and defined in types.go, including
// ExitStatus, which was the last SDK type in the moved code.
//
// The exported API is deliberately NOT shaped to fit ACP's terminal/* wire
// types. internal/acp keeps its own SDK-typed face and does the translation on
// its side, which is mechanical and a little tedious — and that is the intended
// division. Bending a surviving package's API to suit a consumer scheduled for
// deletion is how a dead consumer's shape outlives it by years.
package tmuxhost
