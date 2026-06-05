package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// ContextRendezvousTimeout bounds how long a chunk hook waits for its
// predecessor before emitting anyway. Fault tolerance wins over ordering: a
// crashed or slow predecessor must never hang session startup, so on timeout
// we proceed (accepting possibly-shuffled order) rather than block.
const ContextRendezvousTimeout = 5 * time.Second

const (
	rendezvousPoll = 5 * time.Millisecond
	// rendezvousMarkerFresh ignores a "started" marker left over from a prior
	// SessionStart of the same session; only a marker written within this
	// window counts as "the predecessor of THIS run has started". The event's
	// hooks all fire within milliseconds, so this is generously above the real
	// spread and below the typical gap between sessions.
	rendezvousMarkerFresh = 10 * time.Second
)

// heldRendezvousLocks keeps acquired locks referenced for the lifetime of the
// process so the GC can't finalize (and close) the underlying file early. The
// locks are released by the OS when the hook process exits — which is exactly
// the "I'm done" signal the next chunk hook waits for. We therefore never
// Unlock our own lock.
var heldRendezvousLocks []*flock.Flock

// AwaitTurn blocks until it is part k's turn to emit its context chunk.
//
// Claude Code runs the N SessionStart chunk hooks in parallel and injects their
// output in *completion* order, so to make the model see chunks in order we
// must make the hook processes EXIT in order. We do that with a per-session
// advisory-lock rendezvous (verified airtight under adversarial load):
//
//  1. Acquire this part's own lock and hold it until process exit (the kernel
//     releases an advisory lock only when the holder dies — the cross-process
//     "I'm done" signal a plain file/counter can't provide).
//  2. Publish a fresh "started" marker AFTER locking, so the marker's existence
//     proves the lock is held (closes the startup race where a successor grabs
//     a not-yet-taken lock).
//  3. For part > 1: wait for the predecessor's fresh marker, then block until
//     acquiring the predecessor's lock succeeds — which happens only once the
//     predecessor process has exited.
//
// Fault-tolerant: a no-op when ordering can't or needn't apply (empty
// sessionID, total <= 1, or any filesystem/lock error degrades to "emit now" —
// content still lands, only order may shuffle), and every wait is bounded by
// ContextRendezvousTimeout so a stuck predecessor never blocks startup.
func AwaitTurn(sessionID string, part, total int) {
	if sessionID == "" || total <= 1 || part < 1 {
		return
	}
	dir := rendezvousDir(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return // degrade: emit without ordering rather than fail startup
	}

	own := flock.New(lockPath(dir, part))
	locked, err := own.TryLock()
	if err != nil || !locked {
		// Each part owns a distinct lock file, so contention here means stale
		// state from a crashed run; degrade rather than block.
		return
	}
	heldRendezvousLocks = append(heldRendezvousLocks, own) // released on exit
	writeMarker(dir, part)

	if part == 1 {
		// First chunk emits immediately. Piggyback a best-effort sweep of
		// stale rendezvous dirs left by crashed prior runs (a clean exit
		// releases the locks but leaves the dir behind).
		sweepStaleRendezvous(dir)
		return
	}

	deadline := time.Now().Add(ContextRendezvousTimeout)
	waitFreshMarker(dir, part-1, deadline)
	waitPredecessorExit(dir, part-1, deadline)
}

func rendezvousDir(sessionID string) string {
	return filepath.Join(os.TempDir(), "ctxloom-rdv-"+sanitizeSessionID(sessionID))
}

// rendezvousDirPrefix is the shared prefix of every rendezvous dir, used by
// the GC sweep to recognize its own leftovers.
const rendezvousDirPrefix = "ctxloom-rdv-"

// rendezvousMaxAge bounds how long a rendezvous dir may linger before the
// sweep reclaims it. A clean run's locks release on process exit, but a crash
// can strand the dir; this is far above any real SessionStart spread and below
// any sane gap that would make a live session's dir look stale.
const rendezvousMaxAge = time.Hour

// sweepStaleRendezvous best-effort removes rendezvous dirs older than
// rendezvousMaxAge, skipping `except` (the current session's dir). All errors
// are swallowed: GC must never affect session startup.
func sweepStaleRendezvous(except string) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), rendezvousDirPrefix) {
			continue
		}
		full := filepath.Join(os.TempDir(), e.Name())
		if full == except {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > rendezvousMaxAge {
			_ = os.RemoveAll(full)
		}
	}
}

// sanitizeSessionID keeps only filename-safe characters so a session id can
// never escape the rendezvous directory or inject a path separator.
func sanitizeSessionID(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

func lockPath(dir string, part int) string {
	return filepath.Join(dir, fmt.Sprintf("l%d.lock", part))
}

func markerPath(dir string, part int) string {
	return filepath.Join(dir, fmt.Sprintf("started_%d", part))
}

func writeMarker(dir string, part int) {
	_ = os.WriteFile(markerPath(dir, part), []byte("1"), 0o600)
}

// waitFreshMarker blocks until the predecessor's marker exists AND is recent
// (see rendezvousMarkerFresh), or the deadline passes.
func waitFreshMarker(dir string, part int, deadline time.Time) {
	p := markerPath(dir, part)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) <= rendezvousMarkerFresh {
			return
		}
		time.Sleep(rendezvousPoll)
	}
}

// waitPredecessorExit blocks until the predecessor's lock can be acquired,
// which the OS permits only after the predecessor process has exited. We
// release it again immediately — we only needed the exit signal.
func waitPredecessorExit(dir string, part int, deadline time.Time) {
	prev := flock.New(lockPath(dir, part))
	for time.Now().Before(deadline) {
		if locked, err := prev.TryLock(); err == nil && locked {
			_ = prev.Unlock()
			return
		}
		time.Sleep(rendezvousPoll)
	}
}
