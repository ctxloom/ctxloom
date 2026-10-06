//go:build integration

package integration

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

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

// isolatedLocks redirects the home lock directory to a temp dir for this
// test. Any IN-PROCESS test that applies hooks needs it for the same reason it
// needs isolatedRecords: a locked settings write takes its lock under
// paths.HomeLocksDir, which refuses the developer's real home from a test
// binary rather than leaving a lock file there per run.
func isolatedLocks(t *testing.T) {
	t.Helper()
	t.Cleanup(paths.SetHomeLocksDirForTesting(t.TempDir()))
}
