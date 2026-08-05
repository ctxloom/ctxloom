package layerscope

// Scope names what a key's value is a fact ABOUT — a project, a machine, a
// person, one invocation, or a prior human authorization. It is the whole
// policy: Allows is derived from it, never listed per key, so two keys
// sharing a Scope always share a permission matrix.
type Scope uint8

const (
	// ScopeShared is a fact about the PROJECT, for everyone who clones it: team
	// policy, authored content, a privilege grant a team may legitimately
	// decide once. Settable from project or flag — never home (a home value
	// for a shared key does not "lose the race" in the merge, it FILLS A GAP
	// the project left, which is itself the escalation this package exists to
	// close) and never env (the one channel every spawned child inherits).
	ScopeShared Scope = iota
	// ScopeMachine is a fact about THIS box: its filesystem, binaries,
	// runtimes, credentials-in-transit. Settable from home or env (both are
	// local to one machine) or flag (one invocation on this machine) — never
	// the committed, multi-author project file, which every clone reads
	// regardless of what machine it lands on.
	ScopeMachine
	// ScopePreference is a fact about this PERSON's taste, harmless wherever it
	// is set. Settable from every layer.
	ScopePreference
	// ScopeInvocation is a fact about ONE RUN. Settable from env or flag —
	// never a file, which persists across every future invocation the file
	// itself is loaded for.
	ScopeInvocation
	// ScopeNever is a fact about a prior HUMAN AUTHORIZATION — durable
	// consent, not configuration. No layer may set it; it belongs in an
	// admission store a human's own explicit act writes (see
	// paths.DirtyTreeCommitAckPath), never in the layered config chain a
	// process environment or an argv can also reach.
	ScopeNever
)

// Allows reports whether a value at this Scope may be set at layer l — the
// permission matrix design decision 2 & 6 fix: Shared keeps project+flag only
// (never home, never env); Machine keeps home+env+flag (never the committed
// project file); Preference keeps all four; Invocation keeps env+flag only;
// Never keeps none.
func (s Scope) Allows(l Layer) bool {
	switch s {
	case ScopeShared:
		return l == LayerProject || l == LayerFlag
	case ScopeMachine:
		return l == LayerHome || l == LayerEnv || l == LayerFlag
	case ScopePreference:
		return true
	case ScopeInvocation:
		return l == LayerEnv || l == LayerFlag
	case ScopeNever:
		return false
	default:
		return false
	}
}

// Why is the one sentence a Violation leads with: what this Scope's value is
// a fact about, and why the layer that tried to carry it cannot.
func (s Scope) Why() string {
	switch s {
	case ScopeShared:
		return "this value is a fact about the PROJECT, for everyone who clones it — a committed, multi-author file or a one-invocation flag can carry it; your personal home config or the ambient environment cannot"
	case ScopeMachine:
		return "this value is a fact about THIS machine's filesystem, binaries, or runtimes — your home config, an environment variable, or a one-invocation flag can carry it; the committed project file every clone reads cannot"
	case ScopePreference:
		return "this value is harmless personal taste and may be set at any layer"
	case ScopeInvocation:
		return "this value is a fact about ONE run — an environment variable or a one-invocation flag can carry it; a persisted file, read on every future invocation, cannot"
	case ScopeNever:
		return "this value records a prior HUMAN AUTHORIZATION, not configuration — no config layer may set it"
	default:
		return "unrecognized scope"
	}
}

// String names s for a diagnostic.
func (s Scope) String() string {
	switch s {
	case ScopeShared:
		return "shared"
	case ScopeMachine:
		return "machine"
	case ScopePreference:
		return "preference"
	case ScopeInvocation:
		return "invocation"
	case ScopeNever:
		return "never"
	default:
		return "unknown"
	}
}
