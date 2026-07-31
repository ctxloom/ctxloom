package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLinkTranscriptIntoHarpDir_AbsentTargetIsAnnounced pins that a bind
// pointing at a transcript that is not there produces a WARNING rather than a
// silent dangling symlink.
//
// The link is still created: BindSession fires on MCP initialize and an engine
// may not have flushed its transcript yet, so refusing would throw away a link
// that becomes valid moments later. What must not happen is the current
// outcome, where the harp dir grows a transcript.jsonl that resolves to
// nothing and every later reader gets a bare ENOENT with no hint that the
// binding itself was stale.
func TestLinkTranscriptIntoHarpDir_AbsentTargetIsAnnounced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	target := filepath.Join(t.TempDir(), "never-written.jsonl")
	// The fixture must be hostile from the helper's own vantage point.
	_, statErr := os.Stat(target)
	require.ErrorIs(t, statErr, os.ErrNotExist, "the fixture target must genuinely be absent")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	linkTranscriptIntoHarpDir("swift-amber-falcon", target)

	link := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon", "transcript.jsonl")
	got, err := os.Readlink(link)
	require.NoError(t, err, "the link is still created; only the silence is the defect")
	require.Equal(t, target, got)

	require.NotEmpty(t, sink.String(),
		"a bind pointing at an absent transcript must be announced, not left as a dangling link")
	require.Contains(t, sink.String(), target,
		"the warning must name the path that does not resolve; got %q", sink.String())
}

// TestLinkTranscriptIntoHarpDir_UnremovableExistingLinkIsAnnounced pins the
// other swallowed error. The helper replaces any stale link with a bare
// `_ = os.Remove(link)`, which discards every failure and not merely "it was
// not there". When the removal genuinely fails, the symlink that follows fails
// too -- and reports EEXIST, which describes the symptom and hides the cause.
func TestLinkTranscriptIntoHarpDir_UnremovableExistingLinkIsAnnounced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	if os.Geteuid() == 0 {
		t.Skip("root removes non-empty directories' contents restrictions differently; the probe below needs a real failure")
	}
	home := testsupport.Isolate(t)

	// Occupy the link path with a NON-EMPTY directory: os.Remove fails with
	// ENOTEMPTY, which is emphatically not "absent".
	harpDir := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon")
	link := filepath.Join(harpDir, "transcript.jsonl")
	require.NoError(t, os.MkdirAll(link, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(link, "occupant"), []byte("x"), 0o644))

	// Prove the fixture is hostile: the removal the helper performs must fail
	// for a reason other than absence.
	rmErr := os.Remove(link)
	require.Error(t, rmErr, "the fixture must make os.Remove fail")
	require.NotErrorIs(t, rmErr, os.ErrNotExist, "and fail for a reason that is NOT absence")

	target := filepath.Join(t.TempDir(), "abc.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("{}\n"), 0o644))

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	linkTranscriptIntoHarpDir("swift-amber-falcon", target)

	require.Contains(t, sink.String(), "directory not empty",
		"the real removal failure must be reported, not masked by the symlink's EEXIST; got %q", sink.String())
}
