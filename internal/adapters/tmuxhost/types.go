package tmuxhost

// The vocabulary this package hosts terminals in.
//
// These types exist because this package MUST NOT import the ACP SDK. They
// were previously the SDK's own request/response structs, used directly as
// this code's method signatures, which is what bound tmux hosting to one
// consumer: the terminal/* RPC service. tmux hosting is a general capability
// and the binding was accidental, so the types below are deliberately shaped
// around HOSTING A PROCESS IN A TMUX WINDOW — start it, read what it wrote,
// learn how it ended, take it down — and not around any caller's wire format.
//
// The ACP terminal/* face still translates its SDK types into these on the way
// in and back out on the way through. That translation is a little awkward,
// and that is the correct trade: a consumer scheduled for deletion must not
// leave its shape stamped on the package that outlives it.

// TerminalID addresses one hosted terminal for the rest of its life. It is
// minted by Create and is unique across processes sharing the tmux server, not
// merely within this one — see Terminals.run for the collision this prevents.
type TerminalID string

// ExitStatus is how a hosted command ended. Both fields are pointers and
// exactly one is normally set: a command that exited carries ExitCode, one
// that was killed carries Signal. A nil *ExitStatus means "not finished yet",
// which is a THIRD state and is why this is returned by pointer — a zero
// ExitStatus would be indistinguishable from a clean exit 0.
type ExitStatus struct {
	ExitCode *int
	Signal   *string
}

// EnvVar is one environment entry for a hosted command. It is an ordered slice
// rather than a map because these become repeated `tmux new-window -e` flags,
// and a map would make the resulting argv non-deterministic and so untestable.
type EnvVar struct {
	Name  string
	Value string
}

// Spec describes a terminal to host.
type Spec struct {
	Command string
	Args    []string
	// Cwd is the working directory, or "" to inherit.
	Cwd string
	Env []EnvVar
	// OutputLimit caps the bytes Output returns, keeping the TAIL. nil means
	// no cap. It is retained from Create and applied to every later Output
	// call, because the caller that set the limit is not necessarily the one
	// reading.
	OutputLimit *int
}

// Output is one read of a hosted terminal's captured output.
type Output struct {
	Text string
	// Truncated reports that OutputLimit dropped a prefix of Text. It is
	// carried separately because truncated output is not detectable from the
	// text itself, and a reader that assumed it had the whole stream would
	// silently mis-parse a partial one.
	Truncated bool
	// Exit is nil while the command is still running.
	Exit *ExitStatus
}
