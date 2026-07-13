package triggers

// Outcome is a trigger verdict's classification. These four values are
// exhaustive; ParseVerdicts rejects anything else rather than let an
// unrecognized string silently pass through with some assumed meaning.
type Outcome string

const (
	// Fired means the evidence shows the trigger condition has occurred.
	Fired Outcome = "fired"
	// NotFired means the evidence shows the condition has not occurred yet.
	NotFired Outcome = "not-fired"
	// NeedsInvestigation means the evidence is suggestive but not conclusive
	// — a closer look (a tool-using follow-up; out of scope here) could
	// resolve it one way or the other.
	NeedsInvestigation Outcome = "needs-investigation"
	// CannotDetermine means the trigger is not answerable from evidence
	// available inside this system (e.g. it depends on a person's statement,
	// or an event with no trace in the repository or task store). This is the
	// deliberate escape hatch: without it, a model under pressure to classify
	// guesses — and a false FIRED verdict revives a task that isn't actually
	// ready, which is worse than leaving it parked one more cycle.
	CannotDetermine Outcome = "cannot-determine"
)

// Outcomes lists every valid Outcome, in the order presented to the model and
// to a human reading a report (most to least conclusive).
func Outcomes() []Outcome {
	return []Outcome{Fired, NotFired, NeedsInvestigation, CannotDetermine}
}

// Valid reports whether o is one of the four recognized outcomes.
func (o Outcome) Valid() bool {
	switch o {
	case Fired, NotFired, NeedsInvestigation, CannotDetermine:
		return true
	}
	return false
}

// String satisfies fmt.Stringer so an Outcome prints as its wire value.
func (o Outcome) String() string { return string(o) }

// Verdict is the model's structured judgment for one Deferred task's
// trigger. It is a PROPOSAL for a human to confirm — nothing in this package
// or its ctxloom-side caller ever applies a Verdict to task status.
type Verdict struct {
	HarpID     string   `json:"harp_id"`
	Outcome    Outcome  `json:"outcome"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
	Reasoning  string   `json:"reasoning"`

	// Queries is populated only on a round-1 NeedsInvestigation verdict: the
	// model's request for bounded, whitelisted follow-up evidence (see
	// query.go) that ctxloom executes deterministically before re-asking for
	// a final verdict in round 2. A final verdict (round 2, or a round 1
	// verdict that didn't need escalation) never carries queries.
	Queries []Query `json:"queries,omitempty"`

	// Cached reports whether this verdict was served from ctxloom's local
	// verdict cache rather than produced by a fresh model call this run (see
	// the ctxloom-side cache in internal/operations). ParseVerdicts always
	// resets this to false after unmarshaling — even if a model response
	// happens to include a "cached" field — so stray model output can never
	// masquerade as a real cache hit; only the caller, after an actual cache
	// lookup, sets it true.
	Cached bool `json:"cached,omitempty"`
}

// clampConfidence pins a self-reported confidence into [0,1]. A model
// occasionally emits something outside that range (e.g. a 0-100 scale); that
// is a cosmetic slip in a field nothing branches on, so parsing tolerates it
// rather than rejecting an otherwise-usable verdict over it.
func clampConfidence(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}
