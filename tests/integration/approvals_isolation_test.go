//go:build integration

package integration

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// isolatedApprovals points the USER countersignature store — the home-scoped
// one at ~/.ctxloom/approvals, shared by every project on the machine and
// outliving the process — at a directory of this test's own.
//
// Any in-process test that resolves trust needs it. operations refuses a home
// approvals store outside the OS temp root when it is running under a test
// binary (operations.homeApprovalsDir), so without the redirect the user store
// is built with no directory at all, EffectiveTrust's fail-closed gate fires,
// and every item is withheld: the assertions then read an empty file and the
// test fails for a reason that has nothing to do with its subject.
//
// A fresh empty directory is the right redirect target and not a second way to
// withhold: countersign.NewStore reads a nonexistent dir as "nothing approved
// or rejected yet", which is the state a machine that has never run
// `ctxloom review` is in. Only an UNCONFIGURED store (dir "") trips the gate.
//
// Prefer this to moving $HOME: it redirects exactly the store the decision
// lands in, leaving the home config layer, the trust root and the session
// store where the test found them.
func isolatedApprovals(t *testing.T) {
	t.Helper()
	t.Cleanup(operations.SetHomeApprovalsDirForTesting(t.TempDir()))
}
