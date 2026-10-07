package cli

import (
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// runNoStartupFindings backs --no-startup-findings: the opt-out for delivering
// this launch's startup findings into the started agent's context. Default ON
// is the honest choice — an agent should know the ground it stands on — and
// the opt-out exists for scripted/one-shot runs where the extra context is
// measured waste.
var runNoStartupFindings bool

// withStartupFindings is the resolved launch led by its startup findings
// (operations.WithStartupFindings), over everything this invocation recorded;
// the launch as it is when --no-startup-findings opted out.
func (st *runState) withStartupFindings(deps launch.Deps, l launch.Launch) (launch.Launch, error) {
	if runNoStartupFindings {
		return l, nil
	}
	return operations.WithStartupFindings(st.ctx, App(), deps, l, strictness.Since(strictness.Mark{}))
}
