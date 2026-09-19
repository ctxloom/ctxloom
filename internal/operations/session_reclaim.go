package operations

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// ErrNoAgeBound is the refusal when a caller asks to reclaim aged session data
// without saying how old "aged" is.
//
// IT IS AN ERROR AND NOT A DEFAULT at this layer, whatever default the CLI
// applies above it: this function is the one that deletes, and it must not
// be callable in a shape that deletes with no bound stated at all. If the
// caller did not state one, this reclaims NOTHING and says so.
var ErrNoAgeBound = errors.New("reclaiming aged session data requires an explicit age bound")

// ReclaimScope names WHICH members of a session directory the sweep takes.
// It is the lifetime split the layout already encodes — ephemeral/ is the
// disposable store, persist/ is referenced data — and nothing else: the
// sidecar, the essence and segments/ are never in any scope, because they
// are what make the directory a session rather than what it accumulated.
type ReclaimScope uint8

const (
	// ReclaimEphemeral takes ephemeral/ alone: the regenerable state whose
	// loss costs nothing (paths.EphemeralDirName). The default.
	ReclaimEphemeral ReclaimScope = iota
	// ReclaimEphemeralAndPersist also takes persist/ — transcripts, plans,
	// session-scoped artifacts. Referenced data: open task rows cite paths
	// in it, so this is an explicit opt-in, never a default.
	ReclaimEphemeralAndPersist
)

// members is the ONE declaration of which session-dir subdirectories a
// scope takes; every walk, byte count and removal below ranges over it.
func (s ReclaimScope) members() []string {
	if s == ReclaimEphemeralAndPersist {
		return []string{paths.EphemeralDirName, paths.PersistDirName}
	}
	return []string{paths.EphemeralDirName}
}

// String renders the scope for a report, e.g. "ephemeral/" or
// "ephemeral/ and persist/".
func (s ReclaimScope) String() string {
	m := s.members()
	for i := range m {
		m[i] += "/"
	}
	return strings.Join(m, " and ")
}

// SessionReclaimVerdict is one harp's fate, in the same vocabulary the
// worktree reaper already uses (isolation.WorktreeVerdict) so a reader who
// knows one listing can read the other.
type SessionReclaimVerdict string

const (
	// SessionReclaimable: aged, provably not running, and holding nothing
	// that must be preserved. Removable; not yet acted on.
	SessionReclaimable SessionReclaimVerdict = "reclaimable"
	// SessionReclaimed: the in-scope members were removed.
	SessionReclaimed SessionReclaimVerdict = "reclaimed"
	// SessionSpared: aged and not running, but carrying work that must not be
	// destroyed — a scratch worktree holding uncommitted or unknowable
	// content. REPORTED, never reclaimed.
	SessionSpared SessionReclaimVerdict = "spared"
	// SessionKept: exempted by hand — the session carries a
	// paths.SessionKeepMarkerFileName file. Never touched, under any scope.
	SessionKept SessionReclaimVerdict = "kept"
	// SessionSkipped: still running, its liveness could not be proven, or
	// its store is not a plain directory. Never touched.
	SessionSkipped SessionReclaimVerdict = "skipped"
)

// SessionReclaimCandidate is one harp directory and everything this sweep
// decided about it.
type SessionReclaimCandidate struct {
	// Harp is the owning session's name.
	Harp string `json:"harp"`
	// Dir is the harp directory itself, ~/.ctxloom/sessions/<harp>.
	Dir string `json:"dir"`
	// LastActive is the newest modification time found anywhere in this
	// session (see measureSession). It is what the age bound is compared
	// against.
	LastActive time.Time `json:"last_active"`
	// Bytes is what removing the in-scope members reclaims — the size of
	// what would GO, not of the session.
	Bytes int64 `json:"bytes"`
	// OwnerPID is the pid read out of the session's lock file, for a human.
	// It plays no part in any decision here and must not.
	OwnerPID int `json:"owner_pid,omitempty"`
	// Verdict is what this sweep decided, or — after applying — what happened.
	Verdict SessionReclaimVerdict `json:"verdict"`
	// Reason is the human-readable why. Never empty for spared, kept or
	// skipped.
	Reason string `json:"reason,omitempty"`
}

// SessionReclaimResult is one sweep: what it would remove, or did.
type SessionReclaimResult struct {
	// Applied is true only when the caller asked to act.
	Applied bool `json:"applied"`
	// Cutoff is the age bound this run used. Session data last touched at or
	// before it is in scope; anything newer is counted in Newer.
	Cutoff time.Time `json:"cutoff"`
	// Scope is which session-dir members this run takes.
	Scope string `json:"scope"`
	// Candidates is every aged session holding something in scope, whatever
	// its verdict — a skipped session is reported rather than hidden,
	// because "why did it not free anything" is the question a caller
	// actually has.
	Candidates []SessionReclaimCandidate `json:"candidates"`
	Reclaimed  int                       `json:"reclaimed"`
	Spared     int                       `json:"spared"`
	Kept       int                       `json:"kept"`
	Skipped    int                       `json:"skipped"`
	// Newer counts the sessions holding something in scope that were active
	// since the bound. Counted, not listed: with a defaulted age every
	// `ctxloom clean` runs this sweep, and one line per session in use
	// would bury the rest of the report.
	Newer int `json:"newer"`
	// Bytes is what was (or would be) reclaimed: reclaimable candidates only.
	Bytes int64 `json:"bytes"`
}

// ReclaimAgedSessions removes the in-scope members (ReclaimScope) of every
// session directory that is BOTH older than cutoff AND provably not running.
//
// THE AGE BOUND IS REQUIRED. A zero cutoff returns ErrNoAgeBound having
// scanned nothing and removed nothing — see that error's doc.
//
// IT NEVER REMOVES A SESSION DIRECTORY. What it takes is the disposable
// store (and, opted in, the referenced one); the sidecar, the essence and
// segments/ stay, so the session still lists and still resolves afterwards.
// A directory's bytes are what accumulate; its identity is a few hundred.
//
// LIVENESS COMES FROM THE LOCK, never from the session's sidecar. A sidecar
// cannot tell a crashed session from a live one (a process that died before
// EndSession never records an end), so under it a crashed session reads as
// live permanently — and crashed sessions are exactly the population that
// accumulates. The lock (internal/shared/sessionlock) is free once the owner
// is gone however it went, and Verdict.MayReclaim is true for Dead ALONE:
// held, missing, on an untrusted filesystem, or unreadable all REFUSE.
// "Cannot determine" is never permission. It is the same predicate the
// engine-home reaper (ReapOrphanedSessionHomes) decides by, on purpose: two
// sweeps over one session must not disagree about whether it is running.
//
// THE LOCK IS HELD ACROSS THE REMOVAL. Acquire keeps it until the deferred
// release, so a session resuming under the same harp mid-sweep blocks in
// sessionlock.Hold rather than racing the deletion of the tree it is about to
// write into.
//
// A paths.SessionKeepMarkerFileName file at the top of the session directory
// exempts it entirely, under every scope. It is the one hand-placed
// override, checked before the lock is even probed.
//
// IT IS NOT A BLIND RECURSIVE DELETE, and that distinction is the point. A
// harp's ephemeral dir can hold scratch git worktrees, and one of those can
// hold UNCOMMITTED WORK that exists nowhere else. So every candidate's
// worktrees are triaged first, through the very same classifier the worktree
// leaf uses (isolation.ClassifyHarpWorktrees): a session whose worktrees are
// not all provably safe to remove is SPARED and reported, never reclaimed.
// Reaping is triage, not deletion.
//
// NO SYMLINK IS EVER FOLLOWED. A symlinked harp directory is not a candidate;
// a symlinked store (ephemeral/ or persist/ that is itself a link) skips the
// session and leaves the link in place; a symlink deeper inside a store is
// unlinked with it and its target is never entered — os.RemoveAll unlinks,
// it does not descend. Nothing outside the sessions root is reachable.
//
// Best-effort per candidate: one failure warns and the sweep continues.
func ReclaimAgedSessions(ctx context.Context, g git.Git, cutoff time.Time, scope ReclaimScope, apply bool) (SessionReclaimResult, error) {
	result := SessionReclaimResult{Applied: apply, Cutoff: cutoff, Scope: scope.String()}
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
		c, ok := reclaimOneSession(ctx, g, filepath.Join(root, harp), harp, cutoff, scope, apply)
		if !ok {
			continue
		}
		if c.Verdict == "" {
			result.Newer++
			continue
		}
		switch c.Verdict {
		case SessionReclaimed:
			result.Reclaimed++
			result.Bytes += c.Bytes
		case SessionReclaimable:
			result.Bytes += c.Bytes
		case SessionSpared:
			result.Spared++
		case SessionKept:
			result.Kept++
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

// reclaimOneSession decides, and optionally carries out, one harp's fate.
//
// The second return is false when the session is not a candidate at all —
// nothing in scope to take — and the candidate's Verdict is empty when it is
// newer than the bound: in scope but not aged, counted rather than listed.
// The order of checks is deliberate: the CHEAP, non-destructive ones run
// first, so a session that is too new, or kept by hand, is never probed for
// liveness or worktrees at all.
func reclaimOneSession(ctx context.Context, g git.Git, dir, harp string, cutoff time.Time, scope ReclaimScope, apply bool) (SessionReclaimCandidate, bool) {
	c := SessionReclaimCandidate{Harp: harp, Dir: dir}

	m := measureSession(dir, harp, scope)
	if !m.populated {
		return c, false
	}
	c.Bytes, c.LastActive = m.bytes, m.newest

	if m.newest.After(cutoff) {
		return c, true
	}

	if _, err := os.Lstat(filepath.Join(dir, paths.SessionKeepMarkerFileName)); err == nil {
		c.Verdict = SessionKept
		c.Reason = fmt.Sprintf("it carries a %s marker", paths.SessionKeepMarkerFileName)
		return c, true
	}

	if link := m.symlinked; link != "" {
		c.Verdict = SessionSkipped
		c.Reason = fmt.Sprintf("its %s/ is a symlink, which this sweep never follows", link)
		return c, true
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
		return c, true
	}

	// Triage the scratch worktrees BEFORE any removal. The probe is passed in
	// rather than taken again: we are holding this harp's lock, and probing
	// from under our own hold would read it as a live owner.
	candidates, err := isolation.ClassifyHarpWorktrees(ctx, g, harp, probe)
	if err != nil {
		c.Verdict = SessionSkipped
		c.Reason = fmt.Sprintf("its scratch worktrees could not be classified, so its data is left alone: %v", err)
		return c, true
	}
	if apply {
		candidates = isolation.ReapWorktrees(ctx, g, candidates)
	}
	if held, why := worktreeHoldingWork(candidates); held {
		c.Verdict = SessionSpared
		c.Reason = why
		return c, true
	}

	if !apply {
		c.Verdict = SessionReclaimable
		return c, true
	}
	for _, member := range scope.members() {
		if err := os.RemoveAll(filepath.Join(dir, member)); err != nil {
			clidiag.Warn("ctxloom", "session reclaim: cannot remove %q: %v", filepath.Join(dir, member), err)
			c.Verdict = SessionSkipped
			c.Reason = fmt.Sprintf("its %s/ could not be removed: %v", member, err)
			return c, true
		}
	}
	// The lock FILE is deliberately left behind, and this is not an oversight:
	// unlinking it while we hold it would let a session resuming under this
	// harp create and lock a FRESH inode and believe it owns the session we
	// are still deleting — the exact race holding the lock exists to prevent.
	// It is a handful of bytes; the stores are what accumulate.
	c.Verdict = SessionReclaimed
	return c, true
}

// worktreeHoldingWork reports whether any of a session's scratch worktrees is
// still standing, and why the first such one was left.
//
// ANY survivor spares the whole session. An ephemeral dir that still holds a
// checkout carrying uncommitted work cannot be removed without taking that
// work with it, and work that exists nowhere else outranks the disk the
// sweep would reclaim. Reporting beats reclaiming on every tie.
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

// sessionMeasure is what measureSession learns about one session.
type sessionMeasure struct {
	// bytes is the total size of the in-scope members.
	bytes int64
	// populated is true when at least one in-scope member holds an entry:
	// there is something to reclaim.
	populated bool
	// symlinked names the first in-scope member that is itself a symlink,
	// or "" when none is.
	symlinked string
	// newest is the newest mtime anywhere in the session, including the
	// harp's lock file.
	newest time.Time
}

// measureSession totals the in-scope members' bytes and finds the NEWEST
// modification time anywhere in the harp directory — every member, not only
// the ones in scope — plus the harp's own lock file (which sessionlock stamps
// when the session starts).
//
// The newest mtime is the age signal ON PURPOSE, rather than a recorded
// session-end time. It needs no record — and the sessions that actually
// accumulate are the crashed ones, which are precisely the entries a record
// captures worst. Ranging over the WHOLE session rather than the in-scope
// members is the CONSERVATIVE choice: a plan written to persist/ last week
// protects an ephemeral/ untouched for months, so the sweep errs toward
// keeping data.
//
// Two mtimes are deliberately NOT activity. The harp directory's own: it
// bumps whenever a direct child is created or removed, so counting it would
// make a session this sweep just reaped read as active for another bound —
// and every file under it already carries its own stamp. And a symlink's
// own: it records when the link was made, never a write through it, and the
// target is not followed. WalkDir does not descend into symlinks, and a
// member that is itself a symlink is reported rather than measured.
func measureSession(dir, harp string, scope ReclaimScope) sessionMeasure {
	var m sessionMeasure
	note := func(info fs.FileInfo) {
		if info.Mode()&fs.ModeSymlink != 0 {
			return
		}
		if mt := info.ModTime(); mt.After(m.newest) {
			m.newest = mt
		}
	}
	inScope := map[string]bool{}
	for _, member := range scope.members() {
		inScope[member] = true
		if info, err := os.Lstat(filepath.Join(dir, member)); err == nil && info.Mode()&fs.ModeSymlink != 0 && m.symlinked == "" {
			// A linked store is a candidate — there IS something there —
			// that the sweep will refuse and report, never one it ignores.
			m.symlinked, m.populated = member, true
		}
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // unreadable corner: measured as zero, never a sweep failure
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		note(info)
		top := strings.SplitN(rel, string(filepath.Separator), 2)
		if !inScope[top[0]] {
			return nil
		}
		if len(top) > 1 {
			m.populated = true
		}
		if !d.IsDir() {
			m.bytes += info.Size()
		}
		return nil
	})
	if lock, err := paths.HarpLockPath(harp); err == nil {
		if info, lerr := os.Lstat(lock); lerr == nil {
			note(info)
		}
	}
	return m
}
