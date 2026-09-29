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

	"github.com/gofrs/flock"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/procpin"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// ErrStateOwned reports the project state dir is exclusively owned by another
// live coordinator process. It is exported because the process that loses
// the claim is REFUSED, not degraded: the session host turns it into the
// named finding a second `ctxloom run` on one project exits on.
var ErrStateOwned = errors.New("coord: project state is owned by another live coordinator")

// OwnerMode is how a project's owning session runs.
type OwnerMode string

const (
	// OwnerInteractive is a session a human drives from a terminal — the
	// only kind whose loss of that terminal makes it provably abandoned.
	OwnerInteractive OwnerMode = "interactive"
	// OwnerNonInteractive is a one-shot or internal host: it has no terminal
	// to lose, so nothing about its process proves it abandoned.
	OwnerNonInteractive OwnerMode = "non-interactive"
)

// OwnerStatus is what can be seen of a project's owner without claiming it.
type OwnerStatus struct {
	// Held reports a live process holds the owner lock. It is the kernel's
	// answer (the lock), never a pid probe.
	Held bool
	// PID, Harp, Mode and Started are the holder's stamp; zero when the
	// project is unowned or the stamp is unreadable. For display — and for
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
// file because the lock file cannot carry readable content on every platform:
// Windows' LockFileEx makes the locked range unreadable and unwritable through
// any other handle, so a stamp inside the lock file could be neither written
// after the lock is taken nor read by a prober.
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

const claimRetry = 25 * time.Millisecond

// reclaimTermWait bounds an orphan's own shutdown after SIGTERM; reclaimKillWait
// bounds its death after SIGKILL. Vars so a test can shorten them.
var (
	reclaimTermWait = 10 * time.Second
	reclaimKillWait = 5 * time.Second
)

// claimOwner takes the project state dir's exclusive-owner lock. The journal
// discipline demands a single writer per journal, and that holds ACROSS
// processes too: two concurrent session-owning processes for one project must
// not share journals. The second claimant gets ErrStateOwned and is REFUSED —
// a project has one coordinator, and the loser must not run a rival on state
// of its own (acquireStateDir).
//
// Ownership is a kernel file lock held for the owner's lifetime, so it ends
// exactly when the owning process ends — however it ends — and a pid reused by
// an unrelated process cannot make a dead owner look alive. The lock file is
// never removed: unlinking a locked file lets a claimant that opened the old
// inode and one that creates a new inode both "win".
//
// A held lock is refused unless its holder is provably an abandoned
// interactive session (judgeOrphan); that one is ended and the claim retried.
func claimOwner(rep report.Reporter, dir string, stamp ownerStamp) (release func(), err error) {
	lockPath := filepath.Join(dir, OwnerLockFileName)
	fl, err := lockOwner(lockPath)
	if errors.Is(err, errOwnerLockHeld) {
		st, held := heldOwner(dir)
		if !st.Orphan {
			return nil, ownedError(st)
		}
		if rerr := endOrphan(held); rerr != nil {
			return nil, fmt.Errorf("%w: ending the orphaned owner failed: %w", ownedError(st), rerr)
		}
		rep.Warnf("coordinator: ended the orphaned session %s (pid %d, started %s): %s", st.Harp, st.PID, st.Started.Format(time.RFC3339), st.Reason)
		fl, err = lockOwner(lockPath)
		if errors.Is(err, errOwnerLockHeld) {
			st, _ = heldOwner(dir)
			return nil, ownedError(st)
		}
	}
	if err != nil {
		return nil, err
	}
	stampPath := filepath.Join(dir, ownerStampFileName)
	raw, merr := json.Marshal(stamp)
	if merr == nil {
		merr = iox.WriteFileAtomic(stampPath, raw, 0o600)
	}
	if merr != nil {
		// The lock, not the stamp, is ownership: an unstamped owner is only
		// unidentifiable (never reclaimable, refused by name without a pid).
		rep.Warnf("coordinator: could not stamp the project owner %s (%v); this session owns the project but others cannot see who holds it", stampPath, merr)
	}
	return func() {
		// Unstamp while still holding the lock, so no claimant is mid-stamp.
		_ = os.Remove(stampPath)
		_ = fl.Close()
	}, nil
}

// errOwnerLockHeld is lockOwner's "another process holds it".
var errOwnerLockHeld = errors.New("coord: the owner lock is held")

func lockOwner(lockPath string) (*flock.Flock, error) {
	fl := flock.New(lockPath, flock.SetPermissions(0o600))
	ctx, cancel := context.WithTimeout(context.Background(), claimWait)
	defer cancel()
	got, err := fl.TryLockContext(ctx, claimRetry)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		_ = fl.Close()
		return nil, fmt.Errorf("coord: owner lock %s: %w", lockPath, err)
	}
	if !got {
		_ = fl.Close()
		return nil, errOwnerLockHeld
	}
	return fl, nil
}

// ProbeOwner reports who owns the project state dir without claiming it.
// Held is the kernel's answer; the rest is the holder's stamp and the orphan
// judgement a claim would act on.
func ProbeOwner(dir string) (OwnerStatus, error) {
	lockPath := filepath.Join(dir, OwnerLockFileName)
	if _, err := os.Lstat(lockPath); errors.Is(err, fs.ErrNotExist) {
		return OwnerStatus{}, nil
	}
	// Read-only and never created: a probe must not mint a lock file.
	fl := flock.New(lockPath, flock.SetFlag(os.O_RDONLY))
	got, err := fl.TryLock()
	_ = fl.Close()
	if err != nil {
		return OwnerStatus{}, fmt.Errorf("coord: probe owner lock %s: %w", lockPath, err)
	}
	if got {
		return OwnerStatus{}, nil
	}
	st, _ := heldOwner(dir)
	return st, nil
}

// heldOwner describes a HELD lock from its holder's stamp, and returns the
// stamp itself for the reclaim.
func heldOwner(dir string) (OwnerStatus, ownerStamp) {
	st := OwnerStatus{Held: true}
	raw, err := os.ReadFile(filepath.Join(dir, ownerStampFileName))
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
