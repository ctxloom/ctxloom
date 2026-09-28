// Package sessionlock answers one question for every sweep that reclaims
// per-session data: is the session that owns this harp still running?
//
// The signal is an EXCLUSIVE FILE LOCK (newHarpLock: flock(2) on Linux and
// macOS, LockFileEx over a byte past the content on Windows) that the session-owning process
// holds on paths.HarpLockPath(harp) for as long as it runs. A sweeper tries
// the lock:
//
//	acquires it → the owner is DEAD: the kernel released the lock when the
//	              process ended, however it ended. Reclaiming is permitted.
//	fails       → the owner is ALIVE. Refuse.
//	anything else → INDETERMINATE. Refuse.
//
// It is crash-safe by construction: SIGKILL, an OOM kill, a closed terminal
// and power loss all release the lock without any cleanup path running. No
// pid takes part in the decision, so pid reuse cannot fool it and a
// containerized owner's pid namespace does not matter — same kernel, same
// inode. The file's CONTENT is the holder's pid anyway, so a human reading
// the file can see which process holds it; that is all it is for.
//
// BEST EFFORT IS THE AMBITION, and the shape of the API says so: a verdict is
// only ever used to REFUSE. Verdict.MayReclaim is true for Dead alone. Alive
// refuses, Indeterminate refuses, an untrusted filesystem refuses, an error
// refuses. Live-looking means leave it alone — wrong in the recoverable
// direction, which is the direction a sweep that deletes data must be wrong
// in.
//
// NOT "hold the file open and probe deletability". On Windows an open handle
// blocks deletion, so that probe reads as liveness; on Unix POSIX permits
// unlinking an open file, so the same probe calls a live session dead. The
// LOCK is the portable fact; the open handle is merely how it is held.
package sessionlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// Verdict is what a probe learned about the session that owns a harp.
type Verdict int

const (
	// Indeterminate: the probe could not decide — no lock file (a session
	// from before the lock existed, or one whose Hold failed), an unreadable
	// one, a filesystem whose locks cannot be trusted, or an error. Treated
	// exactly like Alive by every consumer: refuse.
	Indeterminate Verdict = iota
	// Alive: the lock is held by a running process.
	Alive
	// Dead: the lock was acquired, so nothing holds it — the owner ended.
	// The ONLY verdict that permits reclaiming.
	Dead
)

// MayReclaim is the single permission this package grants, and it is granted
// by Dead alone. Every other value — including values that do not exist — is
// a refusal, so a consumer that switches on Verdict cannot fall through into
// a reclaim by omission.
func (v Verdict) MayReclaim() bool { return v == Dead }

func (v Verdict) String() string {
	switch v {
	case Alive:
		return "alive"
	case Dead:
		return "dead"
	default:
		return "indeterminate"
	}
}

// Probe is one answer about one harp. PID is the pid read out of the file's
// content — the last process to Hold it — or 0 when there was none to read;
// it is reported for a human and plays no part in Verdict. Reason is the
// human-readable why, never empty for a refusal.
type Probe struct {
	Verdict Verdict
	PID     int
	Reason  string
}

// ErrHeldElsewhere is Hold's typed failure: another process holds the harp's
// lock and did not release it within holdWait.
var ErrHeldElsewhere = errors.New("sessionlock: the session lock is held by another process")

// holdWait bounds how long Hold waits for a lock somebody else holds. The
// only legitimate other holder is a sweeper mid-reclaim (Acquire keeps the
// lock while it deletes, bounded by its own per-candidate timeouts), so a
// wait this long that still fails is a genuine second owner, not contention.
// A var so a test can shorten it.
var holdWait = 10 * time.Second

// holdRetry is the TryLock polling interval inside holdWait.
const holdRetry = 50 * time.Millisecond

// lockFileMode is the lock file's permission: the content is a pid, which is
// nobody else's business, and the sessions root is already 0700.
const lockFileMode = 0o600

// held is the process-wide set of locks this process holds, keyed by harp.
//
// Process-wide on purpose: the kernel holds the real state per PROCESS, and
// flock(2) refuses a second descriptor on a file the same process already
// locked — so a second registry would make a repeated Hold for one harp fail
// against itself. This map is only the memory of which descriptors to
// release; it is not the lock.
var (
	heldMu sync.Mutex
	held   = map[string]harpLock{}
)

// harpLock is the one lock primitive every holder and probe of a harp lock
// file goes through; newHarpLock builds it per OS. Close releases.
type harpLock interface {
	TryLock() (bool, error)
	TryLockContext(ctx context.Context, retry time.Duration) (bool, error)
	Close() error
}

// Hold takes harp's lock for this process and keeps it until Release (or the
// process ends, which is the point). Idempotent per process.
//
// THE ORDER IS STAMP, THEN LOCK, and both halves are load-bearing:
//
//   - The pid is written IN PLACE on the existing inode
//     (iox.TruncateInPlace, never write-temp-then-rename). A rename would
//     swap the inode under any lock already held on the path, and a sweeper
//     opening the new inode would find it unlocked and read a LIVE session
//     as dead.
//   - It is written BEFORE the lock is taken so no second descriptor is ever
//     opened on a locked file: on the fcntl-emulated platforms closing any
//     descriptor drops the process's lock.
//
// A Hold that loses the lock to another holder for longer than holdWait
// REMOVES the file before returning ErrHeldElsewhere: a file left behind
// would be unlocked once the other holder let go and would read as Dead —
// the one outcome worse than no file, which reads as Indeterminate and is
// refused.
func Hold(harp string) error {
	heldMu.Lock()
	defer heldMu.Unlock()
	if _, ok := held[harp]; ok {
		return nil
	}

	path, err := paths.HarpLockPath(harp)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("sessionlock: create the sessions root for %s: %w", harp, err)
	}
	if err := stampPID(path); err != nil {
		return err
	}

	fl := newHarpLock(path, harpLockFlagCreate)
	ctx, cancel := context.WithTimeout(context.Background(), holdWait)
	defer cancel()
	stop := lockwait.Watch(path)
	got, err := fl.TryLockContext(ctx, holdRetry)
	stop()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		_ = fl.Close()
		_ = os.Remove(path)
		return fmt.Errorf("sessionlock: lock %s: %w", path, err)
	}
	if !got {
		_ = fl.Close()
		_ = os.Remove(path)
		return fmt.Errorf("%w: %s", ErrHeldElsewhere, path)
	}
	held[harp] = fl
	return nil
}

// stampPID writes this process's pid as path's whole content, in place.
//
// iox.TruncateInPlace, NOT iox.WriteFileAtomic, and the choice is the whole
// correctness of the probe: an atomic write renames a fresh temp file over
// the destination, which swaps the INODE. A sweeper mid-Acquire holds its
// flock on the inode it opened; after a rename that inode is unreferenced by
// the path, and the next sweeper to open the path gets a NEW, unlocked inode
// and reads a live session as dead. Truncating in place leaves the inode
// every existing lock is bound to exactly where it was
// (iox.TestWriteFileInPlace_KeepsTheSameInode is the differential proof, and
// TestStampPID_KeepsALockedInodeVisiblyAlive pins it for this call site).
//
// The pid text is never empty, so TruncateInPlace's refusal to write zero
// bytes over an existing file cannot fire here.
func stampPID(path string) error {
	pid := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if err := iox.WriteFileInPlace(path, iox.TruncateInPlace, pid, lockFileMode); err != nil {
		return fmt.Errorf("sessionlock: stamp %s: %w", path, err)
	}
	return nil
}

// Release lets go of harp's lock. The file stays: unlocked IS the dead
// signal, and its pid still tells a human who last ran the session. A harp
// this process does not hold is a no-op.
func Release(harp string) {
	heldMu.Lock()
	defer heldMu.Unlock()
	fl, ok := held[harp]
	if !ok {
		return
	}
	delete(held, harp)
	_ = fl.Close()
}

// Acquire probes harp and, on Dead, KEEPS the lock until the returned
// release is called — so a sweeper reclaims under the lock, and a session
// resumed under the same harp meanwhile waits in Hold instead of racing the
// deletion. On any other verdict the release holds nothing and is harmless.
//
// It never creates the lock file: a file the probe minted would be unlocked
// and read as Dead on the next probe.
func Acquire(harp string) (Probe, func()) {
	noop := func() {}
	path, err := paths.HarpLockPath(harp)
	if err != nil {
		return Probe{Reason: fmt.Sprintf("no lock path: %v", err)}, noop
	}

	trusted, fstype, err := fsTrust(filepath.Dir(path))
	switch {
	case err != nil:
		return Probe{PID: readPID(path), Reason: fmt.Sprintf("the filesystem holding %s could not be identified, so its lock cannot be trusted: %v", path, err)}, noop
	case !trusted:
		return Probe{PID: readPID(path), Reason: fmt.Sprintf("%s is on a %s filesystem, whose locks cannot be trusted to prove the owner dead", path, fstype)}, noop
	}

	if _, err := os.Lstat(path); err != nil {
		return Probe{Reason: fmt.Sprintf("no session lock at %s — the owner cannot be proven dead", path)}, noop
	}

	// O_RDONLY without O_CREATE: the Lstat above already ruled out absence,
	// and a create here would be the race that mints an unlocked file.
	fl := newHarpLock(path, harpLockFlagExisting)
	got, err := fl.TryLock()
	if err != nil {
		_ = fl.Close()
		return Probe{PID: readPID(path), Reason: fmt.Sprintf("probing the session lock %s failed: %v", path, err)}, noop
	}
	if !got {
		_ = fl.Close()
		return Probe{Verdict: Alive, PID: readPID(path), Reason: "the session's lock is held: its owner is alive"}, noop
	}
	return Probe{Verdict: Dead, PID: readPID(path), Reason: "the session's lock was free: its owner has ended"}, func() { _ = fl.Close() }
}

// Inspect is Acquire without keeping anything: the verdict, released at
// once. For listings and previews, never for the reclaim itself.
func Inspect(harp string) Probe {
	p, release := Acquire(harp)
	release()
	return p
}

// readPID reads the pid out of the lock file for reporting. 0 on any doubt;
// it decides nothing.
func readPID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// fsTrust is the filesystem gate Acquire consults, indirected so a test can
// drive the untrusted and unidentifiable cases without a network mount.
var fsTrust = fsTrusted
