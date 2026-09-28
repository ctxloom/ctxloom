package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateTempDir points os.TempDir() at a per-test directory so rendezvous
// state (which lives under os.TempDir()) is created in an auto-cleaned location
// and never collides with another test or a real session.
func isolateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// os.TempDir reads TMPDIR on unix and TMP (then TEMP) on Windows.
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	// AwaitTurn deliberately holds its own lock until process exit (see
	// heldRendezvousLocks), so in a test binary those handles outlive the
	// test. Windows cannot delete a file with an open handle, so TempDir's
	// RemoveAll would fail. Cleanups run LIFO: registered after TempDir, this
	// release runs before the removal.
	t.Cleanup(releaseHeldRendezvousLocks)
	return dir
}

// releaseHeldRendezvousLocks unlocks (and so closes) every lock AwaitTurn is
// holding for the process lifetime — the in-test stand-in for process exit.
func releaseHeldRendezvousLocks() {
	heldRendezvousLocksMu.Lock()
	defer heldRendezvousLocksMu.Unlock()
	for _, l := range heldRendezvousLocks {
		_ = l.Unlock()
	}
	heldRendezvousLocks = nil
}

// assertLockHeld proves the lock at path is held by someone else: a fresh
// handle cannot take it. Portable — both flock(2) and LockFileEx conflict
// across separate handles even within one process.
func assertLockHeld(t *testing.T, path, msg string) {
	t.Helper()
	probe := flock.New(path)
	locked, err := probe.TryLock()
	require.NoError(t, err)
	if locked {
		_ = probe.Unlock()
	}
	assert.False(t, locked, msg)
}

func TestSanitizeSessionID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"alnum_dash_underscore_preserved", "abc-DEF_123", "abc-DEF_123"},
		{"path_separators_replaced", "a/b\\c", "a_b_c"},
		{"dots_and_dotdot_replaced", "../escape", "___escape"},
		{"spaces_and_symbols_replaced", "id with:colon", "id_with_colon"},
		{"empty_stays_empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeSessionID(tt.in))
		})
	}
}

func TestRendezvousPathBuilders(t *testing.T) {
	isolateTempDir(t)
	dir := rendezvousDir("sess-1")
	assert.Equal(t, rendezvousDirPrefix+"sess-1", filepath.Base(dir),
		"dir name is the prefix plus the sanitized session id")
	assert.Equal(t, filepath.Join(dir, "l2.lock"), lockPath(dir, 2))
	assert.Equal(t, filepath.Join(dir, "started_2"), markerPath(dir, 2))
}

func TestWriteMarker(t *testing.T) {
	dir := t.TempDir()
	writeMarker(dir, 1)
	data, err := os.ReadFile(markerPath(dir, 1))
	require.NoError(t, err, "marker file must be written")
	assert.Equal(t, "1", string(data))
}

func TestWaitFreshMarker(t *testing.T) {
	t.Run("returns_fast_when_marker_is_fresh", func(t *testing.T) {
		dir := t.TempDir()
		writeMarker(dir, 1)
		start := time.Now()
		waitFreshMarker(dir, 1, time.Now().Add(2*time.Second))
		assert.Less(t, time.Since(start), 500*time.Millisecond,
			"a fresh marker should return well before the deadline")
	})

	t.Run("waits_until_deadline_when_marker_missing", func(t *testing.T) {
		dir := t.TempDir()
		start := time.Now()
		waitFreshMarker(dir, 1, time.Now().Add(60*time.Millisecond))
		// It can only return by the deadline passing — so it must have waited.
		assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond,
			"a missing marker must block until the deadline")
	})

	t.Run("waits_until_deadline_when_marker_is_stale", func(t *testing.T) {
		dir := t.TempDir()
		writeMarker(dir, 1)
		// Backdate the marker past the freshness window so it is ignored.
		stale := time.Now().Add(-2 * rendezvousMarkerFresh)
		require.NoError(t, os.Chtimes(markerPath(dir, 1), stale, stale))
		start := time.Now()
		waitFreshMarker(dir, 1, time.Now().Add(60*time.Millisecond))
		assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond,
			"a stale marker is not fresh, so it must block until the deadline")
	})
}

func TestWaitPredecessorExit(t *testing.T) {
	t.Run("returns_immediately_when_lock_is_free", func(t *testing.T) {
		dir := t.TempDir()
		start := time.Now()
		waitPredecessorExit(dir, 1, time.Now().Add(2*time.Second))
		assert.Less(t, time.Since(start), 500*time.Millisecond,
			"a free predecessor lock should be acquired at once")
	})

	t.Run("blocks_until_predecessor_releases", func(t *testing.T) {
		dir := t.TempDir()
		held := flock.New(lockPath(dir, 1))
		locked, err := held.TryLock()
		require.NoError(t, err)
		require.True(t, locked, "test must hold the predecessor lock first")

		go func() {
			time.Sleep(40 * time.Millisecond)
			_ = held.Unlock()
		}()

		start := time.Now()
		waitPredecessorExit(dir, 1, time.Now().Add(2*time.Second))
		elapsed := time.Since(start)
		assert.GreaterOrEqual(t, elapsed, 30*time.Millisecond,
			"must wait for the predecessor to release")
		assert.Less(t, elapsed, 2*time.Second,
			"must return once released, not run to the deadline")
	})
}

func TestSweepStaleRendezvous(t *testing.T) {
	tmp := isolateTempDir(t)

	stale := filepath.Join(tmp, rendezvousDirPrefix+"stale")
	fresh := filepath.Join(tmp, rendezvousDirPrefix+"fresh")
	current := filepath.Join(tmp, rendezvousDirPrefix+"current")
	unrelated := filepath.Join(tmp, "some-other-dir")
	for _, d := range []string{stale, fresh, current, unrelated} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	old := time.Now().Add(-2 * rendezvousMaxAge)
	require.NoError(t, os.Chtimes(stale, old, old))
	// `current` is also old, but is the active session dir and must be spared.
	require.NoError(t, os.Chtimes(current, old, old))

	sweepStaleRendezvous(current)

	assert.NoDirExists(t, stale, "a rendezvous dir older than the max age is reclaimed")
	assert.DirExists(t, fresh, "a recent rendezvous dir is kept")
	assert.DirExists(t, current, "the excepted (active) dir is kept even when old")
	assert.DirExists(t, unrelated, "a non-rendezvous dir is never touched")
}

func TestAwaitTurn(t *testing.T) {
	t.Run("no_op_on_guard_conditions", func(t *testing.T) {
		isolateTempDir(t)
		cases := []struct {
			name        string
			sessionID   string
			part, total int
		}{
			{"empty_session", "", 1, 3},
			{"single_total", "s", 1, 1},
			{"part_below_one", "s", 0, 3},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				AwaitTurn(c.sessionID, c.part, c.total)
				if c.sessionID != "" {
					assert.NoDirExists(t, rendezvousDir(c.sessionID),
						"a guard no-op must not create rendezvous state")
				}
			})
		}
	})

	t.Run("part_one_locks_and_marks_then_returns", func(t *testing.T) {
		isolateTempDir(t)
		const sess = "await-part-one"
		start := time.Now()
		AwaitTurn(sess, 1, 3)
		assert.Less(t, time.Since(start), time.Second, "part 1 never waits")

		dir := rendezvousDir(sess)
		assertLockHeld(t, lockPath(dir, 1), "part 1 holds its own lock")
		assert.FileExists(t, markerPath(dir, 1), "part 1 publishes its started marker")
	})

	t.Run("later_part_returns_once_predecessor_marker_and_lock_are_satisfied", func(t *testing.T) {
		isolateTempDir(t)
		const sess = "await-part-two"
		dir := rendezvousDir(sess)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		// Predecessor (part 1) has started (fresh marker) and exited (lock free).
		writeMarker(dir, 1)

		start := time.Now()
		AwaitTurn(sess, 2, 3)
		assert.Less(t, time.Since(start), time.Second,
			"a satisfied predecessor lets part 2 proceed without waiting")

		assert.FileExists(t, markerPath(dir, 2), "part 2 publishes its own marker")
		assertLockHeld(t, lockPath(dir, 2), "part 2 holds its own lock")
	})
}

// TestAwaitTurn_ConcurrentCallsDoNotRaceOnHeldLocks pins the fix:
// heldRendezvousLocks is a package-level slice appended without
// synchronization. A single hook process only ever calls AwaitTurn once
// today, but the package itself provides no guarantee of that, and nothing
// stops it running concurrently within one process (e.g. a future caller, or
// parallel subtests sharing a binary). Each goroutine below uses its own
// session (so no goroutine blocks waiting on another — every one is "part 1",
// which returns immediately) and distinctly named locks, isolating the race
// to the shared heldRendezvousLocks slice itself. Run under `go test -race`
// (test-pkg's default): unsynchronized appends here are a detected data race
// before the fix.
func TestAwaitTurn_ConcurrentCallsDoNotRaceOnHeldLocks(t *testing.T) {
	isolateTempDir(t)
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			AwaitTurn(fmt.Sprintf("race-sess-%d", i), 1, 2)
		}(i)
	}
	wg.Wait()
}

// TestIsolateTempDir_ReleasesHeldLocksBeforeRemoval pins the cleanup order the
// Windows TempDir removal depends on: once a test using isolateTempDir ends,
// no AwaitTurn lock handle may remain open. Portable — it fails on Linux too if
// the release is dropped, where the open-handle delete itself would succeed.
func TestIsolateTempDir_ReleasesHeldLocksBeforeRemoval(t *testing.T) {
	t.Run("holder", func(t *testing.T) {
		isolateTempDir(t)
		AwaitTurn("release-check", 1, 2)
		heldRendezvousLocksMu.Lock()
		n := len(heldRendezvousLocks)
		heldRendezvousLocksMu.Unlock()
		require.Positive(t, n, "AwaitTurn must hold its own lock during the test")
	})
	heldRendezvousLocksMu.Lock()
	defer heldRendezvousLocksMu.Unlock()
	assert.Empty(t, heldRendezvousLocks, "isolateTempDir cleanup must release every held lock")
}
