// Package admission is the one shape every "may this happen" decision in
// ctxloom takes, and the one store that records the human answers behind them.
//
// Three gates arrived at the same design independently — content exposure
// (internal/bundles), companion execution (internal/config) and publish
// destinations (internal/remote) — and two of them grew separate
// trust-on-first-use stores on the same day. This package is that convergence
// stated once, so the FOURTH gate inherits the six properties the three
// currently hold only by coincidence of three good decisions:
//
//  1. THE ZERO VALUE WITHHOLDS. A Decision nobody populated denies, and its
//     Reason is its domain's own zero — which every domain spells "unset".
//     A struct literal can therefore never read as an admission.
//  2. THE DECIDER IS A PURE FUNCTION; THE CALLER RENDERS. Nothing here writes
//     to a terminal. Everything a human must be told comes back on
//     Decision.Detail and the CALLER says it. That is what lets one decision
//     serve a gate, a listing and a report without any of them re-deriving it.
//  3. "NOBODY COULD BE ASKED" AND "YOU DECLINED" ARE DIFFERENT OUTCOMES. They
//     have different fixes — supply a terminal, versus change your mind — so
//     they are different Reasons, and a Store refuses to be built with them
//     spelled the same (see Reasons.validate).
//  4. NON-INTERACTIVE REFUSES. A nil Ask is the non-interactive case and
//     produces Reasons.Unasked, never a prompt written into a pipe and never
//     an assumed yes.
//  5. AN UNRESOLVABLE $HOME REFUSES rather than reading the working directory.
//     filepath.Join("", x) == x, so an unconfigured store would silently key
//     off the process working directory — and since a record's existence is
//     the whole authority, a stray file at a repo root would authorise
//     something. An unconfigured Store answers nothing and writes nothing.
//  6. CONSENT RECORDS ARE PERSONAL. The store is a single file under the
//     user's home; there is deliberately no committable project twin, or a
//     repo you cloned could arrive carrying pre-approved binaries and
//     pre-approved publish destinations.
//
// Each domain keeps its OWN Reason enum as the type argument. That is what the
// generics buy: one flow, one store, one set of properties, and a vocabulary
// per domain that a report and a gate can still share exhaustively.
package admission

// Decision is what an admission decision produced: the answer, WHICH rule
// produced it, and the human-facing elaboration on it.
//
// The Reason is meaningful for admits as well as refusals — "allowed because
// it is builtin" and "allowed because a human countersigned it" are different
// facts, and a review surface needs both. A caller that reports Allow without
// reporting Reason is reporting less than the decision knew.
//
// The zero value is a REFUSAL with the domain's zero Reason. That is load
// bearing: a Decision assembled by a struct literal that forgot a field must
// not read as permission.
type Decision[R comparable] struct {
	// Allow reports whether the thing being decided about may proceed.
	Allow bool
	// Reason names WHICH rule decided, in the domain's own closed vocabulary.
	Reason R
	// Detail is the human-readable elaboration on Reason — a publisher's
	// stated retraction reason, the error behind an unreadable store, the
	// sentence a warning consists of. It is display-only and is NEVER an
	// input to any decision.
	Detail string
}

// Authorizer decides one thing, purely: no sink, no logging, no side effects.
// Everything it wants a human to know comes back on the Decision and the
// CALLER emits it.
type Authorizer[Q any, R comparable] interface {
	Admit(Q) Decision[R]
}

// AuthorizerFunc adapts a plain function to Authorizer, for a decision that is
// genuinely one expression (and for tests). It carries no state, which is the
// tell that a real authorizer — one that opens a consent store — should be a
// named type whose construction says what it reads.
type AuthorizerFunc[Q any, R comparable] func(Q) Decision[R]

// Admit implements Authorizer.
func (f AuthorizerFunc[Q, R]) Admit(q Q) Decision[R] { return f(q) }
