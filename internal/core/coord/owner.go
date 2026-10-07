package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/procpin"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// ErrStateOwned reports a coordinator root is already adopted by another live
// process. Only a claim that NAMES an existing root can meet it — a resume
// (Options.RootHarp) of a session whose tree a live process still holds; a
// fresh session founds a root of its own and never does. The claimant is
// refused rather than share the root's journals.
var ErrStateOwned = errors.New("coord: this coordinator root is owned by another live process")

// OwnerMode is how a root's owning session runs.
type OwnerMode string

const (
	// OwnerInteractive is a session a human drives from a terminal — the
	// only kind whose loss of that terminal makes it provably abandoned.
	OwnerInteractive OwnerMode = "interactive"
	// OwnerNonInteractive is a one-shot or internal host: it has no terminal
	// to lose, so nothing about its process proves it abandoned.
	OwnerNonInteractive OwnerMode = "non-interactive"
)

// OwnerStatus is what can be seen of a root's owner without claiming it.
type OwnerStatus struct {
	// Held reports a live process holds the owner lock. It is the kernel's
	// answer (the lock), never a pid probe.
	Held bool
	// PID, Harp, Mode and Started are the holder's stamp; zero when the
	// root is unowned or the stamp is unreadable. For display — and for
	// the orphan test, which re-establishes the process's identity itself.
	PID     int
	Harp    string
	Mode    OwnerMode
	Started time.Time
	// Orphan reports the holder is provably an abandoned interactive
	// session, which the next claim ends and replaces (claimOwner).
	Orphan bool
	// Reason says why Orphan is what it is, for a held lock.
	Reason string
}

// ownerStampFileName holds the owner's stamp beside the lock. It is a SEPARATE
// file because the stamp is written atomically (safefs.WriteFile: a temp file
// renamed into place), and a rename over the lock file would swap the inode
// the lock is held on: the next claimant would open the new, unlocked inode
// and take a root that is still owned.
const ownerStampFileName = "owner.json"

// ownerStamp is the owner's self-description, written only by the lock holder.
type ownerStamp struct {
	PID     int       `json:"pid"`
	Harp    string    `json:"harp"`
	Mode    OwnerMode `json:"mode"`
	Started time.Time `json:"started"`
	// StartTicks is the kernel's start time for PID (procpin.Stat), so a
	// reader can tell the stamped process from a later holder of its pid.
	// 0 where the platform cannot say, which makes the owner unreclaimable.
	StartTicks uint64 `json:"start_ticks,omitempty"`
}

func newOwnerStamp(harp string, mode OwnerMode) ownerStamp {
	s := ownerStamp{PID: os.Getpid(), Harp: harp, Mode: mode, Started: time.Now().UTC()}
	if st, err := procpin.ReadStat(s.PID); err == nil {
		s.StartTicks = st.StartTicks
	}
	return s
}

// claimWait bounds the wait for the owner lock. The only legitimate
// short-lived holder besides an owner is a ProbeOwner, which releases at once;
// a lock still held after this is an owner.
const claimWait = 500 * time.Millisecond

// reclaimTermWait bounds an orphan's own shutdown after SIGTERM; reclaimKillWait
// bounds its death after SIGKILL. Vars so a test can shorten them.
var (
	reclaimTermWait = 10 * time.Second
	reclaimKillWait = 5 * time.Second
)

// claimOwner takes a root state dir's exclusive-owner lock. The journal
// discipline demands a single writer per journal, and that holds ACROSS
// processes too: two concurrent processes claiming one root must not share
// its journals. The second claimant gets ErrStateOwned and is REFUSED — a
// root has one coordinator, and the loser must not run a rival on state of
// its own (acquireStateDir).
//
// Ownership is a kernel file lock held for the owner's lifetime, so it ends
// exactly when the owning process ends — however it ends — and a pid reused by
// an unrelated process cannot make a dead owner look alive. The lock file is
// never removed: unlinking a locked file lets a claimant that opened the old
// inode and one that creates a new inode both "win".
//
// A held lock is refused unless its holder is provably an abandoned
// interactive session (judgeOrphan); that one is ended and the claim retried.
func claimOwner(root safefs.Root, rep report.Reporter, dir string, stamp ownerStamp) (release func(), err error) {
	lockPath := filepath.Join(dir, OwnerLockFileName)
	lk, err := lockOwner(root.Locks, lockPath)
	if errors.Is(err, errOwnerLockHeld) {
		st, held := heldOwner(root.Fs, dir)
		if !st.Orphan {
			return nil, ownedError(st)
		}
		if rerr := endOrphan(held); rerr != nil {
			return nil, fmt.Errorf("%w: ending the orphaned owner failed: %w", ownedError(st), rerr)
		}
		rep.Warnf("coordinator: ended the orphaned session %s (pid %d, started %s): %s", st.Harp, st.PID, st.Started.Format(time.RFC3339), st.Reason)
		lk, err = lockOwner(root.Locks, lockPath)
		if errors.Is(err, errOwnerLockHeld) {
			st, _ = heldOwner(root.Fs, dir)
			return nil, ownedError(st)
		}
	}
	if err != nil {
		return nil, err
	}
	stampPath := filepath.Join(dir, ownerStampFileName)
	raw, merr := json.Marshal(stamp)
	if merr == nil {
		merr = safefs.WriteFile(root.Fs, stampPath, raw, safefs.PrivateFileMode)
	}
	if merr != nil {
		// The lock, not the stamp, is ownership: an unstamped owner is only
		// unidentifiable (never reclaimable, refused by name without a pid).
		rep.Warnf("coordinator: could not stamp the root owner %s (%v); this session owns the root but others cannot see who holds it", stampPath, merr)
	}
	return func() {
		// Unstamp while still holding the lock, so no claimant is mid-stamp.
		_ = root.Fs.Remove(stampPath)
		_ = lk.Unlock()
	}, nil
}

// errOwnerLockHeld is lockOwner's "another process holds it".
var errOwnerLockHeld = errors.New("coord: the owner lock is held")

// errRootRemoved is lockOwner's "the root went away under this claim": its
// dir is gone, or the lock won is on a file RemoveRoot already unlinked. The
// claim is not refused — the root is simply not there to own — so the
// claimant makes it again and retries (acquireStateDir).
var errRootRemoved = errors.New("coord: the root was removed during the claim")

// Test seams for forcing a claim/removal interleaving: onOwnerLockContended
// fires once a claim has found the lock held and begins to wait for it;
// afterOwnerLock fires once a lock is won, before it is verified.
var (
	onOwnerLockContended = func(lockPath string) {}
	afterOwnerLock       = func(lockPath string) {}
)

// lockOwner takes lockPath's lock, waiting up to claimWait. A lock is only a
// claim on the root if the file it locks is still the one at lockPath: a
// removal (RemoveRoot) unlinks the lock file under its own lock, and a
// claimant that opened the file before that and locks it after holds a lock
// nobody else can see (Lock.Current). Both that and a dir that vanished
// mid-wait are errRootRemoved.
func lockOwner(locks safefs.Locks, lockPath string) (safefs.Lock, error) {
	once, cancelOnce := context.WithCancel(context.Background())
	cancelOnce() // a done context: TryLock makes exactly one attempt
	lk, err := locks.TryLock(once, lockPath)
	if errors.Is(err, safefs.ErrLockHeld) {
		onOwnerLockContended(lockPath)
		ctx, cancel := context.WithTimeout(context.Background(), claimWait)
		lk, err = locks.TryLock(ctx, lockPath)
		cancel()
	}
	switch {
	case errors.Is(err, safefs.ErrLockHeld):
		return nil, errOwnerLockHeld
	case errors.Is(err, fs.ErrNotExist):
		return nil, errRootRemoved
	case err != nil:
		return nil, fmt.Errorf("coord: owner lock %s: %w", lockPath, err)
	}
	afterOwnerLock(lockPath)
	if !lk.Current() {
		_ = lk.Unlock()
		return nil, errRootRemoved
	}
	return lk, nil
}

// whileRemovingRoot is a test seam: RemoveRoot calls it holding the root's
// lock, before the delete.
var whileRemovingRoot = func(dir string) {}

// RemoveRoot deletes a coordinator root — the ONE path that does. It CLAIMS
// the root first: a root a live process holds is refused (ErrStateOwned) and
// left whole. The root is deleted under the lock (removeClaimedRoot), so a
// claimant of the same root either waits it out and then finds the root gone
// (and makes it afresh — errRootRemoved), or makes it afresh in the dir the
// removal was emptying and keeps it, or wins first and is refused nothing. A
// root that does not exist is already removed.
func RemoveRoot(root safefs.Root, projectID, projectDir, rootHarp string) error {
	dir, err := RootStateDir(projectID, projectDir, rootHarp)
	if err != nil {
		return err
	}
	if _, err := root.Fs.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	lk, err := lockOwner(root.Locks, filepath.Join(dir, OwnerLockFileName))
	switch {
	case errors.Is(err, errRootRemoved):
		return nil
	case errors.Is(err, errOwnerLockHeld):
		st, _ := heldOwner(root.Fs, dir)
		return ownedError(st)
	case err != nil:
		return err
	}
	whileRemovingRoot(dir)
	return removeClaimedRoot(root.Fs, dir, lk)
}

// removeClaimedRoot deletes the root dir whose owner lock lk holds, and
// releases lk. The lock file is unlinked LAST: a waiting claimant re-opens
// the lock path with O_CREATE on every poll, so from that unlink on it can
// make a fresh lock file in the dir, win it and find it current — nothing
// deleted after the unlink is deleted under the lock. The only step after it
// is the rmdir, which refuses a dir that is not empty; refused with a lock
// file back at the path, it met a claimant that made the root afresh, and
// that root is left to it. Windows refuses to unlink a file with an open
// handle — the held lock file — so there the unlink is finished after the
// release; a claimant that opened the lock file in between holds a handle
// that refuses the unlink in turn, so the retry can never unlink a lock
// someone holds.
func removeClaimedRoot(fsys afero.Fs, dir string, lk safefs.Lock) error {
	lockPath := filepath.Join(dir, OwnerLockFileName)
	if err := removeAllBut(fsys, dir, OwnerLockFileName); err != nil {
		_ = lk.Unlock()
		return err
	}
	unlinkErr := fsys.Remove(lockPath)
	_ = lk.Unlock()
	if unlinkErr != nil {
		if err := fsys.Remove(lockPath); err != nil {
			return fmt.Errorf("coord: remove root %s: %w", dir, err)
		}
	}
	if err := fsys.Remove(dir); err != nil {
		if _, serr := fsys.Stat(lockPath); serr == nil {
			return nil
		}
		return fmt.Errorf("coord: remove root %s: %w", dir, err)
	}
	return nil
}

// removeAllBut deletes every entry of dir except the one named keep.
func removeAllBut(fsys afero.Fs, dir, keep string) error {
	entries, err := afero.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("coord: remove root %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.Name() == keep {
			continue
		}
		if err := fsys.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("coord: remove root %s: %w", dir, err)
		}
	}
	return nil
}

// ProbeOwner reports who owns a root state dir without claiming it.
// Held is the kernel's answer; the rest is the holder's stamp and the orphan
// judgement a claim would act on.
func ProbeOwner(root safefs.Root, dir string) (OwnerStatus, error) {
	held, err := root.Locks.Held(filepath.Join(dir, OwnerLockFileName))
	if err != nil {
		return OwnerStatus{}, fmt.Errorf("coord: probe owner lock: %w", err)
	}
	if !held {
		return OwnerStatus{}, nil
	}
	st, _ := heldOwner(root.Fs, dir)
	return st, nil
}

// heldOwner describes a HELD lock from its holder's stamp, and returns the
// stamp itself for the reclaim.
func heldOwner(fsys afero.Fs, dir string) (OwnerStatus, ownerStamp) {
	st := OwnerStatus{Held: true}
	raw, err := afero.ReadFile(fsys, filepath.Join(dir, ownerStampFileName))
	var s ownerStamp
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err != nil || s.PID <= 0 {
		st.Reason = fmt.Sprintf("the owner's stamp is unreadable (%v), so the owner cannot be identified", err)
		return st, ownerStamp{}
	}
	st.PID, st.Harp, st.Mode, st.Started = s.PID, s.Harp, s.Mode, s.Started
	st.Orphan, st.Reason = judgeOrphan(s)
	return st, s
}

// readProcStat is the kernel stat judgeOrphan decides on; a seam so tests
// can present any process shape.
var readProcStat = procpin.ReadStat

// judgeOrphan decides whether the stamped holder of a HELD lock is provably an
// abandoned interactive session. Every "cannot establish" is a refusal: only
// an interactive owner whose stamped process still runs, has been reparented
// to init, and has no controlling terminal is an orphan. A session hosted in
// tmux or a pane keeps its pty and never matches.
func judgeOrphan(s ownerStamp) (bool, string) {
	if s.Mode != OwnerInteractive {
		return false, fmt.Sprintf("the owner is not an interactive session (mode %q)", s.Mode)
	}
	if s.StartTicks == 0 {
		return false, "the owner's process identity was not recorded, so it cannot be established"
	}
	ps, err := readProcStat(s.PID)
	if err != nil {
		return false, fmt.Sprintf("the owner process cannot be inspected: %v", err)
	}
	if ps.StartTicks != s.StartTicks {
		return false, fmt.Sprintf("pid %d is no longer the process that stamped the claim", s.PID)
	}
	if ps.PPID != 1 {
		return false, fmt.Sprintf("the owner still has its parent (pid %d)", ps.PPID)
	}
	if ps.TTYNr != 0 {
		return false, "the owner still has a controlling terminal"
	}
	return true, "an interactive session reparented to init with no controlling terminal: its terminal is gone"
}

// errNoLongerOrphan reports an owner that stopped matching judgeOrphan
// between the judgement and the pin, so it was left alone.
var errNoLongerOrphan = errors.New("the owner no longer proves orphaned")

// endOrphan ends an orphaned owner: SIGTERM, a bounded wait, then SIGKILL. A
// seam so tests can stand in for a real process.
var endOrphan = func(s ownerStamp) error {
	h, ok := procpin.Pin(s.PID)
	if !ok {
		return nil // already gone: its lock went with it
	}
	defer h.Close()
	// Re-judge AFTER pinning: the handle can only ever reach the process
	// that existed when it was taken, and the start-time match inside
	// judgeOrphan proves that process is the stamped one.
	if orphan, why := judgeOrphan(s); !orphan {
		return fmt.Errorf("%w: %s", errNoLongerOrphan, why)
	}
	if err := h.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	if h.WaitExit(reclaimTermWait) {
		return nil
	}
	if err := h.Signal(syscall.SIGKILL); err != nil {
		return err
	}
	if !h.WaitExit(reclaimKillWait) {
		return fmt.Errorf("pid %d survived SIGKILL for %s", s.PID, reclaimKillWait)
	}
	return nil
}

// ownedError is the refusal for a held lock, naming the owner when its stamp
// does.
func ownedError(st OwnerStatus) error {
	if st.PID == 0 {
		return fmt.Errorf("%w (%s)", ErrStateOwned, st.Reason)
	}
	return fmt.Errorf("%w (owner session %s, pid %d, started %s)", ErrStateOwned, st.Harp, st.PID, st.Started.Format(time.RFC3339))
}
