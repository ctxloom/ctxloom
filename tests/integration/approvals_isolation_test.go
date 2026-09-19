//go:build integration

package integration

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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
	t.Cleanup(countersign.SetHomeDirForTesting(t.TempDir()))
}

// isolatedRecords points the §9.7 application-record store at a directory of
// this test's own.
//
// Any IN-PROCESS test that applies hooks needs it. Writing .mcp.json goes
// through confpatch, which records what it wrote so the next write can take it
// back out, and paths.HomeRecordsDir roots that store in the developer's REAL
// ~/.ctxloom/records. These helpers run in-process against a temp project, so
// without the redirect each run left a durable record there naming a temp path
// that no longer exists — 1046 of them were found in one developer's home.
//
// paths.HomeRecordsDir now REFUSES an unsandboxed store under a test binary,
// so omitting this is a loud failure rather than a silent deposit. That is the
// point: the guard names the fix while the fix is cheap.
func isolatedRecords(t *testing.T) {
	t.Helper()
	t.Cleanup(paths.SetHomeRecordsDirForTesting(t.TempDir()))
}
