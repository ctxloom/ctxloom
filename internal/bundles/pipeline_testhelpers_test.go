package bundles

// Test-only pipeline constructors. Gating and form selection are process-stage
// policy, so a test that wants either builds the stage that carries it; these
// two spell the two shapes so the intent of each call site is visible.

// ungated wraps a reader in a pipeline that does NOT gate — the
// management/listing shape, and the right one for a test that is exercising
// resolution rather than trust. AdmitAll, never nil: nil is a forgotten gate
// and delivers nothing, which would make every resolution test here fail for a
// reason that has nothing to do with what it is testing.
func ungated(l *Loader, preferDistilled bool) *Pipeline {
	return NewPipeline(l, AdmitAll(), preferDistilled)
}

// gatedPipe wraps a reader in a pipeline that decides with authorizer — the
// exposure shape.
func gatedPipe(l *Loader, authorizer Authorizer, preferDistilled bool) *Pipeline {
	return NewPipeline(l, authorizer, preferDistilled)
}

// authorizerFunc adapts a plain function to Authorizer, so a test can spell a decision
// inline. Test-only: production authorizers are named types whose construction says
// which stores they read.
type authorizerFunc func(Exposure) Verdict

func (f authorizerFunc) Admit(e Exposure) Verdict { return f(e) }

// admitVerdict / denyVerdict spell the two outcomes a test authorizer returns, so
// no test has to pick a Reason it does not care about.
func admitVerdict() Verdict { return Verdict{Allow: true, Reason: ReasonLocal} }
func denyVerdict() Verdict  { return Verdict{Reason: ReasonPending} }
