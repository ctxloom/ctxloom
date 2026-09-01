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
// It names the CAPABILITY — terminals and panes hosted in tmux — rather than
// any consumer. Hosting a process on a pty in tmux is independent of whatever
// happens to drive it, and the pane path (`ctxloom attach`, injection) has its
// own callers entirely.
//
// Every type crossing this package's boundary is local and defined in types.go,
// including ExitStatus. The exported API is deliberately not shaped to fit any
// one consumer's wire types: bending a surviving package's API to suit a single
// caller is how that caller's shape outlives it by years.
package tmuxhost
