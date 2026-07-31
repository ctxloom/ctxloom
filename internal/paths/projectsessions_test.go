package paths

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// withUnresolvableCwd puts the process in a working directory that no longer
// exists, which is the only way os.Getwd() fails in practice: a directory the
// process is sitting in gets removed underneath it (a cleaned-up worktree, a
// reaped scratch dir, a container teardown).
//
// It asserts the fault is real BEFORE handing control back. An isolation
// fixture that silently fails to break anything is indistinguishable from a
// fixed defect — both are green — so the hostility of the fixture is checked
// from the code under test's own vantage point (os.Getwd) rather than assumed.
func withUnresolvableCwd(t *testing.T) {
	t.Helper()

	orig, err := os.Getwd()
	require.NoError(t, err, "need a resolvable cwd to restore afterwards")

	doomed, err := os.MkdirTemp("", "ctxloom-gone-*")
	require.NoError(t, err)

	require.NoError(t, os.Chdir(doomed))
	t.Cleanup(func() {
		// Restore FIRST: every later test in this package resolves paths
		// against the process cwd, and leaving it unresolvable would fail them
		// for a reason that has nothing to do with what they assert.
		_ = os.Chdir(orig)
		_ = os.RemoveAll(doomed)
	})

	require.NoError(t, os.RemoveAll(doomed))

	_, err = os.Getwd()
	require.Error(t, err,
		"the fixture did not make os.Getwd fail, so nothing below exercises the fallback branch")
}

// TestProjectSessionsDir_UnresolvableCwdIsAnnounced pins the fault-tolerance
// contract for ProjectSessionsDir's last resort.
//
// The first two branches return an ANCHORED path — under the supplied app dir,
// or under an absolute cwd. The third returns the bare relative
// ".ctxloom/sessions", which is a categorically different answer: it is
// late-bound, resolved by whoever uses it against whatever the working
// directory is at that moment. A caller that chdirs between this call and its
// read or write silently addresses a different directory.
//
// Degrading is correct — a session dir that cannot be anchored must not block
// startup. Degrading in SILENCE is not: nothing downstream can tell an anchored
// answer from a late-bound one, so the one component that knows must say so.
func TestProjectSessionsDir_UnresolvableCwdIsAnnounced(t *testing.T) {
	withUnresolvableCwd(t)

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	got := ProjectSessionsDir("")

	assert.Equal(t, filepath.Join(AppDirName, SessionsDir), got,
		"the last resort still returns a usable relative path — warn and continue, never block")
	assert.Contains(t, sink.String(), "sessions",
		"the fallback to a cwd-relative sessions dir was taken without telling anyone")
}

// TestProjectSessionsDir_AnchoredBranchesStaySilent is the other half: a
// warning that fires on the ordinary paths would be noise on every invocation,
// and noise is how a real warning stops being read.
func TestProjectSessionsDir_AnchoredBranchesStaySilent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		appDir string
		want   func(t *testing.T) string
	}{
		{
			name:   "explicit app dir",
			appDir: "/project/.ctxloom",
			want:   func(*testing.T) string { return filepath.Join("/project/.ctxloom", SessionsDir) },
		},
		{
			name:   "resolvable cwd",
			appDir: "",
			want: func(t *testing.T) string {
				wd, err := os.Getwd()
				require.NoError(t, err)
				return filepath.Join(wd, AppDirName, SessionsDir)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sink bytes.Buffer
			restore := clidiag.SetSink(&sink)
			defer restore()

			assert.Equal(t, tc.want(t), ProjectSessionsDir(tc.appDir))
			assert.Empty(t, sink.String(), "an anchored resolution has nothing to report")
		})
	}
}
