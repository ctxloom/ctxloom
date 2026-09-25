package bundles

import (
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Test-only pipeline constructors. Gating and form selection are process-stage
// policy, so a test that wants either builds the stage that carries it; these
// two spell the two shapes so the intent of each call site is visible.

// ledger is the real rendering sink, for a test that asserts on the strictness
// ledger or the clidiag stream exactly as the binary's user would see them.
func ledger() report.Sink { return strictness.Sink("ctxloom") }

// admitAllPipe wraps a reader in a pipeline whose authorizer admits everything —
// the right shape for a test exercising resolution rather than trust. Never
// nil: nil is a forgotten gate and delivers nothing, which would fail every
// resolution test here for a reason unrelated to what it tests.
func admitAllPipe(l *Loader, preferDistilled bool) *Pipeline {
	return NewPipeline(l, admitAllForTest(), LinksUnchecked(), preferDistilled)
}

// admitAllForTest is this package's TEST-ONLY allow-all authorizer. No
// production authorizer admits everything; cross-package tests use
// internal/testsupport/admitall, which this package cannot import (it
// imports bundles). The allow is an ordinary admit through Decide's full
// path, ref parse included.
func admitAllForTest() Authorizer {
	return authorizerFunc(func(Exposure) Verdict { return admitVerdict() })
}

// gatedPipe wraps a reader in a pipeline that decides with authorizer — the
// exposure shape.
func gatedPipe(l *Loader, authorizer Authorizer, preferDistilled bool) *Pipeline {
	return NewPipeline(l, authorizer, LinksUnchecked(), preferDistilled)
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
