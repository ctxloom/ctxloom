package transcript

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// tryLockProbe makes one exclusive attempt on path, as another process would,
// and always hands back a release func that is safe to call unconditionally,
// whether or not anything was acquired. It creates path's parent directory
// first: TryLock never does.
func tryLockProbe(t *testing.T, path string) (unlock func(), acquired bool) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	once, cancel := context.WithCancel(context.Background())
	cancel()
	lk, err := safefs.New().Locks.TryLock(once, path)
	if errors.Is(err, safefs.ErrLockHeld) {
		return func() {}, false
	}
	require.NoError(t, err)
	return func() { _ = lk.Unlock() }, true
}

// This file pins the Recorder half of the easeful-dial fix (taskloom
// easeful-dial, fs-consolidation plan slice C7) in isolation from
// operations.RefreshVendorTranscript's half — see
// internal/adapters/operations/vendorreader_ownership_test.go for the end-to-end
// scenario across both.

// TestRecorder_DefaultPath_HoldsSharedOwnershipLockUntilClose asserts a
// default-path Recorder (no WithWriter — the shape
// coord/enginehost constructs for a live structured session) takes the shared ownership
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

	rec, err := NewRecorder(safefs.New(), harp, "claude-code")
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

// TestRecorder_WithWriter_TakesNoOwnershipLock asserts the OTHER half of the
// design: a WithWriter recorder takes NO lock on the canonical transcript. A
// vendor re-conversion (operations.convertVendorTranscript) fills one while
// holding that transcript's EXCLUSIVE lock, so a shared lock taken here would
// block it against its own writer.
//
// Mutation kill: taking the lock before ensureFile's writer branch makes the
// TryLock probe below wrongly fail (acquired=false) while this recorder is
// open — red.
func TestRecorder_WithWriter_TakesNoOwnershipLock(t *testing.T) {
	testsupport.Isolate(t)
	harp := "lock-free-harp"
	var sink bytes.Buffer

	rec, err := NewRecorder(safefs.New(), harp, "claude-code", WithWriter(&sink))
	require.NoError(t, err)

	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeUser, Content: "hello",
	}}))
	require.NotZero(t, sink.Len(), "the line went to the writer")

	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	unlock, acquired := tryLockProbe(t, paths.PathFor(canonPath))
	assert.True(t, acquired, "a WithWriter recorder must not hold the canonical transcript's ownership lock")
	unlock()

	require.NoError(t, rec.Close())
}

// TestRecorder_LocksThroughItsRootsLocks: the ownership lock is taken
// through the Root the recorder was handed, never a fixed one, so over an
// in-memory Root it contends with that Root's other takers — here a rebuild's
// exclusive attempt, which must be refused while the recorder is open.
func TestRecorder_LocksThroughItsRootsLocks(t *testing.T) {
	testsupport.Isolate(t)
	harp := "mem-root-harp"
	files := safefs.NewMem(afero.NewMemMapFs())
	rec, err := NewRecorder(files, harp, "claude-code")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "hello"}}))

	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	once, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = files.Locks.TryLock(once, paths.PathFor(canonPath))
	require.ErrorIs(t, err, safefs.ErrLockHeld, "the recorder's shared lock must be its Root's")
}
