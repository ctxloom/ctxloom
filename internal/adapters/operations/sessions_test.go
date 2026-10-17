package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBindSession_TransientIndexReadFailureWarnsRatherThanFailingSilently:
// BindSession used to discard mgr.Find's error entirely, so a
// transient read failure (a malformed sidecar, here standing in for any
// read/parse fault) was indistinguishable from "no entry for this
// harp" — both took the same silent no-op. First-bind-wins never retries, so
// a harp that misses its bind this way never gets a session id again. The
// SessionStart hook must still never fail the host backend (CLAUDE.md fault
// tolerance), so the fix is a warning, not a returned error.
func TestBindSession_TransientIndexReadFailureWarnsRatherThanFailingSilently(t *testing.T) {
	testsupport.Isolate(t)

	sidecar, err := paths.HarpSidecarPath("some-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(sidecar), 0o755))
	// Malformed YAML (unterminated quote) makes the sidecar parse fail, so
	// mgr.Find returns a genuine parse error, not "absent".
	require.NoError(t, os.WriteFile(sidecar, []byte(`project_dir: ["unterminated`), 0o644))

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	err = BindSession("some-harp", "sess-1", "/tmp/transcript.jsonl")
	require.NoError(t, err, "the SessionStart hook must never fail the host backend")
	assert.Contains(t, buf.String(), "some-harp", "the failure must be warned, naming the harp")
	assert.Contains(t, buf.String(), "session record", "the warning must say what failed")
}

// The sibling of the case above, and the reason both need a test: BindSession
// distinguishes "the index could not be read" (warn, above) from "this harp has
// no entry" (silent no-op, here), and the two used to be the same branch. A
// mutation run found this one NOT COVERED — no test executed the `entry == nil`
// guard at all, so nothing would have noticed it inverting and letting an
// unknown harp through to mgr.BindSession.
//
// Asserts the EFFECT rather than the nil error: a no-op that still wrote a
// binding would satisfy `require.NoError` perfectly well.
func TestBindSession_UnknownHarpWritesNothing(t *testing.T) {
	testsupport.Isolate(t)

	// A VALID store that simply does not hold the harp being bound — so
	// mgr.Find returns (nil, nil), the branch under test, rather than an error.
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	other, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	before, err := os.ReadDir(root)
	require.NoError(t, err)
	require.NotEmpty(t, before, "an empty fixture would make the comparison below vacuous")

	require.NoError(t, BindSession("absent-harp", "sess-1", "/tmp/transcript.jsonl"),
		"the SessionStart hook must never fail the host backend")

	after, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after),
		"binding a harp with no record must write nothing at all")
	assert.NoDirExists(t, filepath.Join(root, "absent-harp"),
		"no session may be minted for a harp the store never knew")
	got, err := mgr.Find(other.HarpName)
	require.NoError(t, err)
	assert.Empty(t, got.SessionID, "and no session id may be recorded against any other session")
}
