package sessions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/sessions"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// rmwDoc is the tiny JSON shape the tests below read-modify-write, standing
// in for a real settings file's managed-name list.
type rmwDoc struct {
	Managed []string `json:"managed"`
}

func readRMWDoc(t *testing.T, path string) rmwDoc {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var d rmwDoc
	require.NoError(t, json.Unmarshal(b, &d))
	return d
}

func writeRMWDoc(t *testing.T, path string, d rmwDoc) {
	t.Helper()
	b, err := json.Marshal(d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o644))
}

// TestWithFileLock_SerializesRMW_BothWritersEntriesSurvive pins the whole
// point of C6: two writers of the SAME file, each appending its OWN entry
// based on a read-modify-write cycle, must not lose one another's
// contribution to a race. Without the lock, writer B's read can be STALE
// relative to writer A's write, so B's write (based on the stale read)
// silently discards A's — this is the lost update D6/D7 describe for
// ~/.claude/settings.json and .mcp.json.
//
// The seam is deterministic, not wall-clock: writer A holds the EXACT same
// home lock WithFileLock itself takes (paths.HomePathFor(target)),
// acquired directly, before writer B's real WithFileLock call is ever
// spawned. That physically prevents B's call from completing until A
// releases, regardless of goroutine scheduling — the only place a small
// sleep appears is a liveness nicety (giving a BROKEN implementation time to
// prove itself broken by letting B run unexcluded), not a mechanism this
// test relies on for correctness.
//
// MUTATION KILL: drop the lock from WithFileLock, leaving it just
// `return fn()`, and this test goes red —
// writer B's goroutine completes its read-modify-write immediately (nothing
// blocks it), tripping the "B completed while A still held the lock"
// assertion below, deterministically within the grace window.
func TestWithFileLock_SerializesRMW_BothWritersEntriesSurvive(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"managed":[]}`), 0o644))
	root := safefs.New()

	appendAndWrite := func(name string) error {
		return sessions.WithFileLock(root.Locks, target, func() error {
			d := readRMWDoc(t, target)
			d.Managed = append(d.Managed, name)
			writeRMWDoc(t, target, d)
			return nil
		})
	}

	// Writer A: take the real home lock directly, standing in for
	// WithFileLock already being mid-critical-section.
	lockPath, err := paths.HomePathFor(target)
	require.NoError(t, err)
	aLock, err := root.Locks.Lock(lockPath)
	require.NoError(t, err)

	bDone := make(chan error, 1)
	go func() { bDone <- appendAndWrite("writer-b") }()

	// Grace window: give a CORRECT implementation nothing to prove (B stays
	// blocked the whole time regardless), and give a BROKEN one enough time
	// to complete unexcluded — read+modify+write of a few bytes takes
	// microseconds, so this is generous, not tight.
	select {
	case <-bDone:
		t.Fatal("writer B completed its read-modify-write while writer A still held the lock — the lock is not excluding concurrent writers")
	case <-time.After(20 * time.Millisecond):
	}

	// Writer A's own critical section, performed manually while holding the
	// SAME lock writer B is blocked on.
	d := readRMWDoc(t, target)
	d.Managed = append(d.Managed, "writer-a")
	writeRMWDoc(t, target, d)
	require.NoError(t, aLock.Unlock())

	require.NoError(t, <-bDone)

	got := readRMWDoc(t, target)
	assert.ElementsMatch(t, []string{"writer-a", "writer-b"}, got.Managed,
		"both writers' managed entries must survive a serialized RMW; a lost update means one silently overwrote the other")
}

// TestWithFileLock_FailsClosedOnLockAcquisitionError pins the fail-closed
// stance WithFileLock shares with config.Owner.Update: a lock
// ACQUISITION failure (as opposed to ordinary blocking on contention, which
// Locks.Lock already waits out) must propagate as an error, and fn must
// NEVER run — degrading to an unlocked read-modify-write on that failure
// would silently discard the one guarantee this function exists to provide,
// on exactly the environmental-fault path where writing unlocked is least
// safe.
//
// The lock path is made impossible to open as a lock file by pre-creating it
// as a DIRECTORY: os.OpenFile(..., O_RDWR) on a directory fails
// deterministically (EISDIR) on every platform this runs on, with no
// permission tricks needed.
func TestWithFileLock_FailsClosedOnLockAcquisitionError(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "settings.json")
	original := []byte(`{"hello":"world"}`)
	require.NoError(t, os.WriteFile(target, original, 0o644))

	lockPath, err := paths.HomePathFor(target)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(lockPath, 0o755))

	called := false
	err = sessions.WithFileLock(safefs.New().Locks, target, func() error {
		called = true
		return nil
	})

	require.Error(t, err)
	assert.False(t, called, "fn must never run when the lock could not be acquired — fail closed, never unlocked")

	after, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, original, after, "the protected file must be byte-identical after a failed lock acquisition")
}

// TestWithFileLock_TakesTheHomeLockAroundFn pins what WithFileLock locks and
// when, deterministically: the lock at paths.HomePathFor(target) is taken
// before fn runs and released after it — whatever Locks it is handed. There
// is no skip: a test's in-memory Locks are locked exactly as the
// controller's own are.
//
// MUTATION KILL: drop the lock (`return fn()`) and the order is just "fn";
// lock any other path and the recorded path differs.
func TestWithFileLock_TakesTheHomeLockAroundFn(t *testing.T) {
	testsupport.Isolate(t)
	target := "/probe/project/.mcp.json"
	want, err := paths.HomePathFor(target)
	require.NoError(t, err)

	rec := &recordingLocks{Locks: safefs.NewMem(afero.NewMemMapFs()).Locks}
	err = sessions.WithFileLock(rec, target, func() error {
		rec.events = append(rec.events, "fn")
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"lock " + want, "fn", "unlock " + want}, rec.events)
}

// TestWithFileLock_AnInMemoryRootLocksOnItsOwnFs: a test's in-memory Locks
// keep the lock file on their own filesystem, so the transaction never
// reaches the real disk.
func TestWithFileLock_AnInMemoryRootLocksOnItsOwnFs(t *testing.T) {
	testsupport.Isolate(t)
	target := "/probe/project/.mcp.json"
	lockPath, err := paths.HomePathFor(target)
	require.NoError(t, err)
	mem := afero.NewMemMapFs()

	require.NoError(t, sessions.WithFileLock(safefs.NewMem(mem).Locks, target, func() error { return nil }))

	ok, err := afero.Exists(mem, lockPath)
	require.NoError(t, err)
	assert.True(t, ok, "the lock file is on the in-memory filesystem")
	_, statErr := os.Stat(lockPath)
	assert.True(t, os.IsNotExist(statErr), "an in-memory Root must never create a REAL lock file")
}

// recordingLocks records each Lock and each Unlock it hands out.
type recordingLocks struct {
	safefs.Locks
	events []string
}

func (r *recordingLocks) Lock(path string) (safefs.Lock, error) {
	r.events = append(r.events, "lock "+path)
	l, err := r.Locks.Lock(path)
	if err != nil {
		return nil, err
	}
	return recordingLock{Lock: l, r: r, path: path}, nil
}

type recordingLock struct {
	safefs.Lock
	r    *recordingLocks
	path string
}

func (l recordingLock) Unlock() error {
	l.r.events = append(l.r.events, "unlock "+l.path)
	return l.Lock.Unlock()
}
