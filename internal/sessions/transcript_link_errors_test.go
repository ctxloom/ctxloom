package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLinkEngineTranscript_AbsentTargetIsAnnounced pins that a bind pointing
// at a transcript that is not there produces a WARNING rather than a silent
// dangling symlink.
//
// The link is still created: BindSession fires on MCP initialize and an
// engine may not have flushed its transcript yet, so refusing would throw
// away a link that becomes valid moments later. What must not happen is the
// current outcome, where the harp dir grows an engine-transcript link that
// resolves to nothing and every later reader gets a bare ENOENT with no hint
// that the binding itself was stale.
func TestLinkEngineTranscript_AbsentTargetIsAnnounced(t *testing.T) {
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

	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target)

	link := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon", "engine-transcript-claude-code-sess-1.jsonl")
	got, err := os.Readlink(link)
	require.NoError(t, err, "the link is still created; only the silence is the defect")
	require.Equal(t, target, got)

	require.NotEmpty(t, sink.String(),
		"a bind pointing at an absent transcript must be announced, not left as a dangling link")
	require.Contains(t, sink.String(), target,
		"the warning must name the path that does not resolve; got %q", sink.String())
}

// TestLinkEngineTranscript_UninspectableExistingEntryIsAnnounced replaces the
// old model's "unremovable existing link" pin: the new contract never
// unconditionally removes what's at the link name (there is no window where
// it repoints unless it can first prove the same name already resolves
// somewhere else), so the failure mode changes shape but not its
// requirement -- a real obstruction at the link path must be reported, not
// masked by an opaque downstream error.
//
// The fixture occupies the link path with a NON-EMPTY directory: os.Readlink
// on a directory fails for a reason that is not absence, and that failure
// must be the one clidiag reports.
func TestLinkEngineTranscript_UninspectableExistingEntryIsAnnounced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	harpDir := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon")
	link := filepath.Join(harpDir, "engine-transcript-claude-code-sess-1.jsonl")
	require.NoError(t, os.MkdirAll(link, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(link, "occupant"), []byte("x"), 0o644))

	// Prove the fixture is hostile: Readlink on a directory must fail, and
	// not with "not exist" (which would route this down the ordinary
	// first-sighting path instead).
	_, rlErr := os.Readlink(link)
	require.Error(t, rlErr, "the fixture must make os.Readlink fail")
	require.NotErrorIs(t, rlErr, os.ErrNotExist, "and fail for a reason that is NOT absence")

	target := filepath.Join(t.TempDir(), "abc.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("{}\n"), 0o644))

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target)

	require.Contains(t, sink.String(), "could not inspect",
		"the real inspection failure must be reported, not masked by a downstream EEXIST; got %q", sink.String())
}

// TestLinkEngineTranscript_SameNameDifferentTargetReplacesAtomicallyAndWarns
// is the same-name-different-target mutation kill: a session id getting
// reused for a DIFFERENT transcript file (never expected in production, but
// the one shape this function DOES repoint for) triggers an atomic replace
// (atomicSymlink) plus a diagnostic naming the anomaly -- an operator finding
// two different histories under one session id needs to know.
func TestLinkEngineTranscript_SameNameDifferentTargetReplacesAtomicallyAndWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	target1 := filepath.Join(t.TempDir(), "first.jsonl")
	require.NoError(t, os.WriteFile(target1, []byte("{}\n"), 0o644))
	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target1)

	link := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon", "engine-transcript-claude-code-sess-1.jsonl")
	got1, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, target1, got1)

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	target2 := filepath.Join(t.TempDir(), "second.jsonl")
	require.NoError(t, os.WriteFile(target2, []byte("{}\n"), 0o644))
	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target2)

	got2, err := os.Readlink(link)
	require.NoError(t, err, "the link must exist and resolve after the replace")
	require.Equal(t, target2, got2, "the replace must land the NEW target")

	require.NotEmpty(t, sink.String(), "a session-id reuse anomaly must be announced")
	require.Contains(t, sink.String(), "sess-1", "the warning must name the reused session id")
	require.Contains(t, sink.String(), target1, "the warning must name the target it replaced")
	require.Contains(t, sink.String(), target2, "the warning must name the target it replaced with")
}

// TestAtomicSymlink_FailureDuringReplaceLeavesOriginalLinkIntact is the
// atomicity mutation kill named in the brief as the deterministic seam: it
// proves atomicSymlink never touches the ORIGINAL link entry before its
// replacement is fully built, by forcing the temp-symlink creation step
// (Symlink(target, tmpName), the step BEFORE the atomic rename) to fail
// deterministically -- a symlink target long enough to exceed the
// filesystem's max symlink content length, independent of directory
// permissions or any race timing.
//
// A Remove(link)-then-Symlink(target, link) mutant reaching the equivalent
// failure would already have removed the original link before the doomed
// create, leaving link ABSENT. This implementation never removes link itself
// (only the disposable reserved temp name), so on failure the original must
// still read back exactly as it did before the call.
func TestAtomicSymlink_FailureDuringReplaceLeavesOriginalLinkIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	origTarget := filepath.Join(dir, "orig.jsonl")
	require.NoError(t, os.WriteFile(origTarget, []byte("{}\n"), 0o644))
	require.NoError(t, os.Symlink(origTarget, link))

	// A target string well past a symlink's max content length (4096 on the
	// common Linux filesystems) forces os.Symlink to fail deterministically.
	hostileTarget := filepath.Join(dir, strings.Repeat("x", 1<<16))

	err := atomicSymlink(hostileTarget, link)
	require.Error(t, err, "the fixture must make the temp symlink step fail")

	got, rlErr := os.Readlink(link)
	require.NoError(t, rlErr, "the original link must still exist after a failed replace")
	require.Equal(t, origTarget, got,
		"the original link must still point at its ORIGINAL target -- atomicSymlink must never touch it before a doomed create fails")
}
