package operations

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// ErrNoAgeBound is the refusal when a caller asks to reclaim aged session data
// without saying how old "aged" is.
//
// IT IS AN ERROR AND NOT A DEFAULT, and that is the whole safety property of
// this sweep. Session records, approvals and application records are LOCAL-ONLY
// — nothing rebuilds them — which is why `clean` never took them before. A
// default age would mean some invocation, somewhere, silently eats history
// nobody asked it to touch. If the caller did not state a bound, this reclaims
// NOTHING and says so.
var ErrNoAgeBound = errors.New("reclaiming aged session data requires an explicit age bound")

// SessionReclaimVerdict is one harp's fate, in the same vocabulary the
// worktree reaper already uses (isolation.WorktreeVerdict) so a reader who
// knows one listing can read the other.
type SessionReclaimVerdict string

const (
	// SessionReclaimable: aged, provably not running, and holding nothing
	// that must be preserved. Removable; not yet acted on.
	SessionReclaimable SessionReclaimVerdict = "reclaimable"
	// SessionReclaimed: the harp directory was removed.
	SessionReclaimed SessionReclaimVerdict = "reclaimed"
	// SessionSpared: aged and not running, but carrying work that must not be
	// destroyed — a scratch worktree holding uncommitted or unknowable
	// content. REPORTED, never reclaimed.
	SessionSpared SessionReclaimVerdict = "spared"
	// SessionSkipped: newer than the bound, still running, or its liveness
	// could not be proven. Never touched.
	SessionSkipped SessionReclaimVerdict = "skipped"
)

// SessionReclaimCandidate is one harp directory and everything this sweep
// decided about it.
type SessionReclaimCandidate struct {
	// Harp is the owning session's name.
	Harp string `json:"harp"`
	// Dir is the harp directory itself, ~/.ctxloom/sessions/<harp>.
	Dir string `json:"dir"`
	// LastActive is the newest modification time found for this session (see
	// sessionLastActive). It is what the age bound is compared against.
	LastActive time.Time `json:"last_active"`
	// Bytes is what removing the harp directory reclaims.
	Bytes int64 `json:"bytes"`
	// OwnerPID is the pid read out of the session's lock file, for a human.
	// It plays no part in any decision here and must not.
	OwnerPID int `json:"owner_pid,omitempty"`
	// Verdict is what this sweep decided, or — after applying — what happened.
	Verdict SessionReclaimVerdict `json:"verdict"`
	// Reason is the human-readable why. Never empty for spared or skipped.
	Reason string `json:"reason,omitempty"`
}

// SessionReclaimResult is one sweep: what it would remove, or did.
type SessionReclaimResult struct {
	// Applied is true only when the caller asked to act.
	Applied bool `json:"applied"`
	// Cutoff is the age bound this run used. Session data last touched at or
	// before it is in scope; anything newer is skipped.
	Cutoff time.Time `json:"cutoff"`
	// Candidates is every harp directory considered, whatever its verdict —
	// a skipped session is reported rather than hidden, because "why did it
	// not free anything" is the question a caller actually has.
	Candidates []SessionReclaimCandidate `json:"candidates"`
	Reclaimed  int                       `json:"reclaimed"`
	Spared     int                       `json:"spared"`
	Skipped    int                       `json:"skipped"`
	// Bytes is what was (or would be) reclaimed: reclaimable candidates only.
	Bytes int64 `json:"bytes"`
}

// ReclaimAgedSessions removes the harp directories of sessions that are BOTH
// older than cutoff AND provably not running.
//
// THE AGE BOUND IS REQUIRED. A zero cutoff returns ErrNoAgeBound having
// scanned nothing and removed nothing — see that error's doc.
//
// LIVENESS COMES FROM THE LOCK, never from the session index's nil EndedAt.
// The index cannot tell a crashed session from a live one (a process that
// died before EndSession keeps a nil EndedAt forever), so under the index a
// crashed session reads as live permanently — and crashed sessions are
// exactly the population that accumulates. The lock (internal/shared/
// sessionlock) is free once the owner is gone however it went, and
// Verdict.MayReclaim is true for Dead ALONE: held, missing, on an untrusted
// filesystem, or unreadable all REFUSE. "Cannot determine" is never
// permission.
//
// THE LOCK IS HELD ACROSS THE REMOVAL. Acquire keeps it until the deferred
// release, so a session resuming under the same harp mid-sweep blocks in
// sessionlock.Hold rather than racing the deletion of the tree it is about to
// write into.
//
// IT IS NOT A RECURSIVE DELETE OVER THE HARP DIRECTORY, and that distinction
// is the point. A harp's ephemeral dir can hold scratch git worktrees, and one
// of those can hold UNCOMMITTED WORK that exists nowhere else. So every
// candidate's worktrees are triaged first, through the very same classifier
// the worktree leaf uses (isolation.ClassifyHarpWorktrees): a session whose
// worktrees are not all provably safe to remove is SPARED and reported, never
// reclaimed. Reaping is triage, not deletion.
//
// Best-effort per candidate: one failure warns and the sweep continues.
func ReclaimAgedSessions(ctx context.Context, g git.Git, cutoff time.Time, apply bool) (SessionReclaimResult, error) {
	result := SessionReclaimResult{Applied: apply, Cutoff: cutoff}
	if cutoff.IsZero() {
		return SessionReclaimResult{}, ErrNoAgeBound
	}
	if g == nil {
		g = git.NewExec()
	}

	root, err := paths.HomeSessionsDir()
	if err != nil {
		return result, fmt.Errorf("resolve sessions dir: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // nothing has ever run here
		}
		return result, fmt.Errorf("scan %q: %w", root, err)
	}

	for _, e := range entries {
		harp := e.Name()
		if !isHarpDirCandidate(e, harp) {
			continue
		}
		c := reclaimOneSession(ctx, g, filepath.Join(root, harp), harp, cutoff, apply)
		switch c.Verdict {
		case SessionReclaimed:
			result.Reclaimed++
			result.Bytes += c.Bytes
		case SessionReclaimable:
			result.Bytes += c.Bytes
		case SessionSpared:
			result.Spared++
		default:
			result.Skipped++
		}
		result.Candidates = append(result.Candidates, c)
	}
	return result, nil
}

// isHarpDirCandidate is the allow-shape: a candidate is a real DIRECTORY
// directly under the sessions root whose name is a valid harp.
//
// A symlink is never followed — following one would turn a sweep of a
// disposable session directory into a delete of whatever it points at — and
// the files beside the harp directories (lock files, the retired index) are
// excluded by "is a directory" alone.
//
// Deliberately BROADER than sessions.IsSessionDir: a directory that has lost
// its sidecar (what `session remove` leaves) is no longer a session, but it
// is still disposable state under the root that an age-bounded reclaim
// exists to sweep. The reclaim decides by age and liveness, never by whether
// the listing would show the directory.
func isHarpDirCandidate(e fs.DirEntry, name string) bool {
	if e.Type()&fs.ModeSymlink != 0 {
		return false
	}
	if !e.IsDir() {
		return false
	}
	return harpid.Validate(name) == nil
}

// reclaimOneSession decides, and optionally carries out, one harp's fate. The
// order is deliberate: the CHEAP, non-destructive checks run first, so a
// session that is too new or still running is never probed for worktrees at
// all.
func reclaimOneSession(ctx context.Context, g git.Git, dir, harp string, cutoff time.Time, apply bool) SessionReclaimCandidate {
	c := SessionReclaimCandidate{Harp: harp, Dir: dir}

	bytes, lastActive := sessionSize(dir, harp)
	c.Bytes, c.LastActive = bytes, lastActive

	// Newer than the bound: out of scope entirely. Checked before liveness so
	// the common case costs no lock probe.
	if lastActive.After(cutoff) {
		c.Verdict = SessionSkipped
		c.Reason = fmt.Sprintf("last active %s, which is newer than the age bound %s",
			lastActive.UTC().Format(time.RFC3339), cutoff.UTC().Format(time.RFC3339))
		return c
	}

	// THE LOCK ONLY EVER REFUSES. Dead alone passes; Alive, Indeterminate and
	// any value that does not exist yet fall out here rather than falling
	// THROUGH toward a removal by omission.
	probe, release := sessionlock.Acquire(harp)
	defer release()
	c.OwnerPID = probe.PID
	if !probe.Verdict.MayReclaim() {
		c.Verdict = SessionSkipped
		c.Reason = probe.Reason
		return c
	}

	// Triage the scratch worktrees BEFORE any removal. The probe is passed in
	// rather than taken again: we are holding this harp's lock, and probing
	// from under our own hold would read it as a live owner.
	candidates, err := isolation.ClassifyHarpWorktrees(ctx, g, harp, probe)
	if err != nil {
		c.Verdict = SessionSkipped
		c.Reason = fmt.Sprintf("its scratch worktrees could not be classified, so its data is left alone: %v", err)
		return c
	}
	if apply {
		candidates = isolation.ReapWorktrees(ctx, g, candidates)
	}
	if held, why := worktreeHoldingWork(candidates); held {
		c.Verdict = SessionSpared
		c.Reason = why
		return c
	}

	if !apply {
		c.Verdict = SessionReclaimable
		return c
	}
	if err := os.RemoveAll(dir); err != nil {
		clidiag.Warn("ctxloom", "session reclaim: cannot remove %q: %v", dir, err)
		c.Verdict = SessionSkipped
		c.Reason = fmt.Sprintf("its directory could not be removed: %v", err)
		return c
	}
	// The lock FILE is deliberately left behind, and this is not an oversight:
	// unlinking it while we hold it would let a session resuming under this
	// harp create and lock a FRESH inode and believe it owns the session we
	// are still deleting — the exact race holding the lock exists to prevent.
	// It is a handful of bytes; the directory is what accumulates.
	c.Verdict = SessionReclaimed
	return c
}

// worktreeHoldingWork reports whether any of a session's scratch worktrees is
// still standing, and why the first such one was left.
//
// ANY survivor spares the whole session. A harp directory whose ephemeral dir
// still holds a checkout carrying uncommitted work cannot be removed without
// taking that work with it, and work that exists nowhere else outranks the
// disk the sweep would reclaim. Reporting beats reclaiming on every tie.
func worktreeHoldingWork(candidates []isolation.WorktreeCandidate) (bool, string) {
	for _, wt := range candidates {
		if wt.Verdict == isolation.VerdictReaped {
			continue
		}
		reason := wt.Reason
		if reason == "" {
			reason = "it is still on disk"
		}
		return true, fmt.Sprintf("its scratch worktree %s must be preserved: %s", filepath.Base(wt.Path), reason)
	}
	return false, ""
}

// sessionSize totals a harp directory's bytes and finds the NEWEST
// modification time anywhere in it, including the harp's own lock file (which
// sessionlock stamps when the session starts).
//
// The newest mtime is the age signal ON PURPOSE, rather than the session
// index's StartedAt/EndedAt. It needs no index — and the sessions that
// actually accumulate are the crashed ones, which are precisely the entries an
// index records worst. It is also the CONSERVATIVE choice: anything touched
// recently protects the whole session, so the sweep errs toward keeping data.
func sessionSize(dir, harp string) (int64, time.Time) {
	var total int64
	var newest time.Time
	note := func(info fs.FileInfo) {
		if mt := info.ModTime(); mt.After(newest) {
			newest = mt
		}
	}
	if info, err := os.Lstat(dir); err == nil {
		note(info)
	}
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // unreadable corner: measured as zero, never a sweep failure
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		note(info)
		if !d.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if lock, err := paths.HarpLockPath(harp); err == nil {
		if info, lerr := os.Lstat(lock); lerr == nil {
			note(info)
		}
	}
	return total, newest
}
