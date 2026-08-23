package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// tryLockProbe attempts a non-blocking exclusive lock on path, mirroring
// flock.Flock.TryLock's own (bool, error) shape but always handing back a
// release func that is safe to call unconditionally, whether or not
// anything was actually acquired — the same "defer it immediately" shape
// every real lock call site in this codebase uses. It creates path's parent
// directory first (flock.New does not, unlike the old filelock.TryLock's
// internal ensureDir), matching what every real call site does before
// acquiring.
func tryLockProbe(t *testing.T, path string) (unlock func(), acquired bool) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	fl := flock.New(path)
	ok, err := fl.TryLock()
	require.NoError(t, err)
	return func() { _ = fl.Unlock() }, ok
}

// This file pins the Recorder half of the easeful-dial fix (taskloom
// easeful-dial, fs-consolidation plan slice C7) in isolation from
// operations.RefreshVendorTranscript's half — see
// internal/operations/vendorreader_ownership_test.go for the end-to-end
// scenario across both.

// TestRecorder_DefaultPath_HoldsSharedOwnershipLockUntilClose asserts a
// default-path Recorder (no WithPath override — the shape
// internal/lm/grpc/chat.go and internal/agentcoord/coord/enginehost.go
// construct for a live structured/ACP session) takes the shared ownership
// lock on ITS OWN canonical path once it actually opens the file (first
// successful Record, per ensureFile's lazy-open contract), holds it for as
// long as the file stays open, and releases it in Close — proven by probing
// the exact same lock file with an independent exclusive TryLock probe.
//
// Mutation kill: removing the LockShared call from ensureFile (or failing to
// clear r.unlock so Close's release is a no-op) makes the first TryLock probe
// below wrongly succeed (acquired=true) while the recorder is still open —
// red.
func TestRecorder_DefaultPath_HoldsSharedOwnershipLockUntilClose(t *testing.T) {
	testsupport.Isolate(t)
	harp := "lock-holding-harp"

	rec, err := NewRecorder(harp, "codex")
	require.NoError(t, err)

	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)

	// Before the first Record, nothing has opened the file yet (NewRecorder
	// is lazy) — a probe must succeed.
	unlock, acquired := tryLockProbe(t, paths.PathFor(canonPath))
	require.True(t, acquired, "an unopened recorder must not hold the lock yet")
	unlock()

	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeUser, Content: "hello",
	}}))

	// Now the file is open: an independent exclusive probe must be refused.
	_, acquired = tryLockProbe(t, paths.PathFor(canonPath))
	assert.False(t, acquired, "a live default-path recorder must hold the canonical transcript's ownership lock")

	require.NoError(t, rec.Close())

	// Close must have released it.
	unlock, acquired = tryLockProbe(t, paths.PathFor(canonPath))
	assert.True(t, acquired, "Close must release the ownership lock")
	unlock()
}

// TestRecorder_WithPathOverride_TakesNoOwnershipLock asserts the OTHER half
// of the design: a WithPath-overridden recorder (the segment and
// rebuild-temp writers operations.convertVendorTranscript constructs) takes
// NO lock on the path it writes to. Nothing else ever contends for a fresh
// per-rebuild temp/segment file, so a lock there would cost a syscall for no
// exclusion anybody needs — and, more importantly, taking one keyed on the
// TEMP path rather than the canonical DEST path would not even provide the
// exclusion the design relies on.
//
// Mutation kill: locking unconditionally in ensureFile (dropping the
// `if r.defaultPath` guard) makes the TryLock probe below wrongly fail
// (acquired=false) while this WithPath recorder is open — red.
func TestRecorder_WithPathOverride_TakesNoOwnershipLock(t *testing.T) {
	testsupport.Isolate(t)
	harp := "lock-free-harp"
	tmpPath := t.TempDir() + "/rebuild.tmp"

	rec, err := NewRecorder(harp, "codex", WithPath(tmpPath))
	require.NoError(t, err)

	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeUser, Content: "hello",
	}}))

	unlock, acquired := tryLockProbe(t, paths.PathFor(tmpPath))
	assert.True(t, acquired, "a WithPath-overridden recorder must not hold a lock on the file it writes")
	unlock()

	require.NoError(t, rec.Close())
}
