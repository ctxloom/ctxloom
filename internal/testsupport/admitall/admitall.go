// Package admitall is a TEST-ONLY allow-all trust gate, for a test that
// exercises resolution, hooks or MCP extraction rather than trust.
//
// Production has no authorizer that admits everything: a Trust is built by
// composite.NewTrust over real ports, and a zero Trust's nil authorizer is
// withheld by bundles.Decide (ReasonUngoverned). This package lives under
// internal/testsupport so the archtestsupport analyzer
// (internal/shared/archlint.TestSupportAnalyzer) fails any shipped binary
// that reaches it.
package admitall

import (
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
)

// Authorizer admits every exposure. The allow is an ordinary one: it goes
// through bundles.Decide's full path, ref parse included, and names
// bundles.ReasonLocal because a test using it does not care which reason.
func Authorizer() bundles.Authorizer { return allowAll{} }

// Trust is a composite.Trust over Authorizer, through the composite.Gated
// seam, for a test that binds a whole Trust (config.BindTrustForTesting).
func Trust() composite.Trust { return composite.Gated(Authorizer()) }

type allowAll struct{}

func (allowAll) Admit(bundles.Exposure) bundles.Verdict {
	return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
}
