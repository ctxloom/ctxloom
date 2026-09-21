package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
)

// ErrNoAgeBound is the refusal when a caller asks to reap aged session data
// without saying how old "aged" is.
//
// IT IS AN ERROR AND NOT A DEFAULT at this layer, whatever default the CLI
// applies above it: Reap is the one function that deletes, and it must not
// be callable in a shape that deletes with no bound stated at all.
var ErrNoAgeBound = errors.New("reaping aged session data requires an explicit age bound")

// ReapPolicy is THE reaper's policy value. Lifetime is the only axis it
// knows; tiers exist for purge classification and doctor reporting.
type ReapPolicy struct {
	// Cutoff is the age bound. Required: a zero value refuses (ErrNoAgeBound).
	// A session whose ActivityTime is at or before it is aged.
	Cutoff time.Time
	// Scope is the widest Lifetime the policy takes. Zero reads as
	// paths.Ephemeral, the default; paths.Persist is a human's
	// --include-persist and TAKES the transcripts with the rest of persist/ —
	// there is no transcript-sparing arm.
	Scope paths.Lifetime
	// Apply is the plan/act switch. False reports the same verdicts and
	// bytes and moves nothing.
	Apply bool
}

func (p ReapPolicy) scope() paths.Lifetime {
	if p.Scope == 0 {
		return paths.Ephemeral
	}
	return p.Scope
}

// Members is what the policy takes from each aged session, in table order:
// every top-level paths.HarpMembers row whose Lifetime is Ephemeral, and
// under Scope Persist the persist store besides — the directory the
// InPersist rows live in, taken whole. A store takes what lives in it, so
// only top-level rows are listed. The identity rows, the essence, the next
// step and the segments are never taken under any scope: a reaped session
// still lists and resolves.
func (p ReapPolicy) Members() []paths.HarpMember {
	var out []paths.HarpMember
	for _, m := range paths.HarpMembers {
		if m.Location != paths.AtTop {
			continue
		}
		if m.Lifetime == paths.Ephemeral || (p.scope() == paths.Persist && m.Name == paths.InPersist.Dir()) {
			out = append(out, m)
		}
	}
	return out
}

// MemberRels is Members as session-dir-relative paths, for a report.
func (p ReapPolicy) MemberRels() []string {
	members := p.Members()
	rels := make([]string, len(members))
	for i, m := range members {
		rels[i] = m.Rel()
	}
	return rels
}

// ReapVerdict is one session's fate under a reap.
type ReapVerdict string

const (
	// ReapReclaimable: aged, provably not running, and holding nothing that
	// must be preserved. Removable; not yet acted on.
	ReapReclaimable ReapVerdict = "reclaimable"
	// ReapReclaimed: the policy's members were removed.
	ReapReclaimed ReapVerdict = "reclaimed"
	// ReapSpared: aged and not running, but the triage found work under the
	// members that must not be destroyed. REPORTED, never reclaimed.
	ReapSpared ReapVerdict = "spared"
	// ReapKept: exempted by hand — the session carries the keep marker.
	// Never touched, under any scope.
	ReapKept ReapVerdict = "kept"
	// ReapSkipped: still running, its liveness could not be proven, a member
	// is a symlink, or a removal failed. Never touched.
	ReapSkipped ReapVerdict = "skipped"
)

// ReapCandidate is one harp directory and everything the reap decided about
// it.
type ReapCandidate struct {
	Harp string `json:"harp"`
	Dir  string `json:"dir"`
	// LastActive is the session's ActivityTime — what the age bound is
	// compared against.
	LastActive time.Time `json:"last_active"`
	// Bytes is what removing the policy's members reclaims — the size of what
	// would GO, not of the session.
	Bytes int64 `json:"bytes"`
	// OwnerPID is the pid read out of the session's lock file, for a human.
	// It plays no part in any decision.
	OwnerPID int `json:"owner_pid,omitempty"`
	// Verdict is what the reap decided, or — after applying — what happened.
	Verdict ReapVerdict `json:"verdict"`
	// Reason is the human-readable why. Never empty for spared, kept or
	// skipped.
	Reason string `json:"reason,omitempty"`
}

// Report is one reap: what it would remove, or did.
type Report struct {
	Applied bool      `json:"applied"`
	Cutoff  time.Time `json:"cutoff"`
	// Members is what the policy takes from each aged session
	// (ReapPolicy.MemberRels).
	Members []string `json:"members"`
	// Candidates is every aged session holding something the policy takes,
	// whatever its verdict — a skipped session is reported rather than
	// hidden, because "why did it not free anything" is the question a
	// caller actually has.
	Candidates []ReapCandidate `json:"candidates"`
	Reclaimed  int             `json:"reclaimed"`
	Spared     int             `json:"spared"`
	Kept       int             `json:"kept"`
	Skipped    int             `json:"skipped"`
	// Newer counts the sessions holding something in scope that were active
	// since the bound. Counted, not listed: with a defaulted age every
	// `ctxloom clean` runs this reap, and one line per session in use would
	// bury the rest of the report.
	Newer int `json:"newer"`
	// Bytes is what was (or would be) reclaimed: reclaimable candidates only.
	Bytes int64 `json:"bytes"`
}

// Triage is the reaper's one question to the outside before it removes an
// aged, provably-dead session's members: is there anything under them that
// must be preserved? This package cannot answer it — a scratch worktree
// holding uncommitted work is a git question — so the adapter that can is
// handed in. A non-empty spared reason spares the session; an error skips
// it ("cannot determine" is never permission). It runs under the reaper's
// hold of the session lock and is handed the reaper's own probe, because
// probing again from under that hold would read the reaper as a live owner.
// On apply it may remove what it judged safe (a registered worktree is
// unregistered, not merely unlinked). Nil spares nothing.
type Triage func(ctx context.Context, harp string, probe LockProbe, apply bool) (spared string, err error)

// Reap is THE reaper. It removes, from every session under the layout that
// is BOTH aged (ActivityTime at or before p.Cutoff) AND provably not
// running, exactly the members p takes (ReapPolicy.Members) — the table
// decides, nothing else.
//
// IT NEVER REMOVES A SESSION DIRECTORY. The identity rows, the essence and
// the segments stay under every scope, so the session still lists and
// resolves afterwards.
//
// LIVENESS COMES FROM THE LOCK (Locks), never from the sidecar: a sidecar
// cannot tell a crashed session from a live one, and crashed sessions are
// exactly the population that accumulates. Dead alone passes; anything else
// REFUSES. The lock is HELD ACROSS THE REMOVAL, so a session resuming under
// the same harp mid-reap waits rather than racing the deletion.
//
// The keep marker row exempts a session entirely, under every scope; it is
// checked before the lock is even probed.
//
// NO SYMLINK IS EVER FOLLOWED. A symlinked harp directory is not a
// candidate; a member that is itself a symlink skips the session and is
// left in place; a symlink deeper inside a member is unlinked with it and
// its target never entered (os.RemoveAll unlinks, it does not descend).
//
// Best-effort per candidate: one failure is that candidate's verdict and
// the reap continues. Cancellation of ctx stops the walk.
func Reap(ctx context.Context, l Layout, locks Locks, p ReapPolicy, triage Triage) (Report, error) {
	if p.Cutoff.IsZero() {
		return Report{}, ErrNoAgeBound
	}
	rep := Report{Applied: p.Apply, Cutoff: p.Cutoff, Members: p.MemberRels()}

	root := l.SessionsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil // nothing has ever run here
		}
		return rep, fmt.Errorf("scan %q: %w", root, err)
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if !isHarpDir(e) {
			continue
		}
		c, ok := reapOne(ctx, l, locks, e.Name(), p, triage)
		if !ok {
			continue
		}
		switch c.Verdict {
		case "":
			rep.Newer++
			continue
		case ReapReclaimed:
			rep.Reclaimed++
			rep.Bytes += c.Bytes
		case ReapReclaimable:
			rep.Bytes += c.Bytes
		case ReapSpared:
			rep.Spared++
		case ReapKept:
			rep.Kept++
		default:
			rep.Skipped++
		}
		rep.Candidates = append(rep.Candidates, c)
	}
	return rep, nil
}

// isHarpDir is the reap's allow-shape: a real DIRECTORY directly under the
// sessions root whose name harp.Validate accepts. A symlink is never
// followed, and the files beside the harp directories (the lock files) are
// excluded by "is a directory" alone.
//
// Deliberately BROADER than the identity predicate (the sidecar's presence):
// a directory that has lost its sidecar is no longer a session, but it is
// still disposable state under the root that an age-bounded reap exists to
// take. The reap decides by age and liveness, never by whether the listing
// would show the directory.
func isHarpDir(e fs.DirEntry) bool {
	if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
		return false
	}
	return harp.Validate(e.Name()) == nil
}

// reapOne decides, and when p.Apply carries out, one session's fate.
//
// The second return is false when the session is not a candidate at all —
// nothing the policy takes is there — and the candidate's Verdict is empty
// when it is newer than the bound: in scope but not aged, counted rather
// than listed. The order of checks is deliberate: the CHEAP, non-destructive
// ones run first, so a session that is too new, or kept by hand, is never
// probed for liveness or triaged at all.
func reapOne(ctx context.Context, l Layout, locks Locks, name string, p ReapPolicy, triage Triage) (ReapCandidate, bool) {
	c := ReapCandidate{Harp: name, Dir: l.Dir(name)}
	members := p.Members()

	m := measureMembers(l, name, members)
	if !m.populated {
		return c, false
	}
	c.Bytes = m.bytes

	last, err := ActivityTime(l, name)
	if err != nil {
		c.Verdict = ReapSkipped
		c.Reason = fmt.Sprintf("its activity could not be read: %v", err)
		return c, true
	}
	c.LastActive = last
	if last.After(p.Cutoff) {
		return c, true
	}

	if _, err := os.Lstat(l.KeepMarker(name)); err == nil {
		c.Verdict = ReapKept
		c.Reason = fmt.Sprintf("it carries a %s marker", paths.SessionKeepMarkerFileName)
		return c, true
	}

	if m.symlinked != "" {
		c.Verdict = ReapSkipped
		c.Reason = fmt.Sprintf("its %s is a symlink, which a reap never follows", m.symlinked)
		return c, true
	}

	// THE LOCK ONLY EVER REFUSES. Dead alone passes; anything else falls
	// out here rather than falling THROUGH toward a removal by omission.
	probe, release := locks.Acquire(name)
	defer release()
	c.OwnerPID = probe.PID
	if !probe.Dead {
		c.Verdict = ReapSkipped
		c.Reason = probe.Reason
		return c, true
	}

	if triage != nil {
		spared, err := triage(ctx, name, probe, p.Apply)
		if err != nil {
			c.Verdict = ReapSkipped
			c.Reason = fmt.Sprintf("it could not be triaged, so its data is left alone: %v", err)
			return c, true
		}
		if spared != "" {
			c.Verdict = ReapSpared
			c.Reason = spared
			return c, true
		}
	}

	if !p.Apply {
		c.Verdict = ReapReclaimable
		return c, true
	}
	for _, member := range members {
		if err := os.RemoveAll(l.Member(name, member)); err != nil {
			c.Verdict = ReapSkipped
			c.Reason = fmt.Sprintf("its %s could not be removed: %v", member.Rel(), err)
			return c, true
		}
	}
	// The lock FILE is deliberately left behind: unlinking it while we hold
	// it would let a session resuming under this harp create and lock a
	// FRESH inode and believe it owns the session we are still deleting —
	// the exact race holding the lock exists to prevent.
	c.Verdict = ReapReclaimed
	return c, true
}

// memberMeasure is what measureMembers learns about the members a policy
// takes from one session.
type memberMeasure struct {
	bytes     int64
	populated bool
	// symlinked names the first taken member that is itself a symlink, or
	// "" when none is.
	symlinked string
}

// measureMembers totals the taken members' bytes and reports whether any of
// them holds anything — a member that is absent, or a directory with no
// entry, is nothing to reclaim. A member that is itself a symlink is a
// candidate (there IS something there) that the reap will refuse and report,
// never one it ignores. WalkDir does not descend into symlinks.
func measureMembers(l Layout, harp string, members []paths.HarpMember) memberMeasure {
	var m memberMeasure
	for _, member := range members {
		p := l.Member(harp, member)
		info, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if m.symlinked == "" {
				m.symlinked = member.Rel()
			}
			m.populated = true
			continue
		}
		if !info.IsDir() {
			m.populated = true
			m.bytes += info.Size()
			continue
		}
		_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || q == p {
				return nil // unreadable corner: measured as zero, never a reap failure
			}
			m.populated = true
			if d.Type().IsRegular() {
				if fi, ierr := d.Info(); ierr == nil {
					m.bytes += fi.Size()
				}
			}
			return nil
		})
	}
	return m
}

// ActivityTime is the ONE clock a session's age is judged by: the newest
// mtime anywhere under the session dir, EXCLUDING the harp directory's own
// mtime and every symlink's mtime. Every list-by-activity and every reap
// reads this and nothing else.
//
// The two exclusions are the point. The harp directory's mtime bumps
// whenever a direct child is created or removed — a reap that just emptied
// it, a lock probe — so counting it would make a session read as active for
// another whole bound after nothing happened in it. A symlink's own mtime
// records when the link was made, never a write through it, and the target
// is not followed. The lock file lives BESIDE the session dir and is stamped
// by every probe; it is not under the dir and does not count.
//
// A missing or unreadable session dir is an error: the caller decides what
// to fall back to.
func ActivityTime(l Layout, harp string) (time.Time, error) {
	dir := l.Dir(harp)
	info, err := os.Lstat(dir)
	if err != nil {
		return time.Time{}, err
	}
	if !info.IsDir() {
		return time.Time{}, fmt.Errorf("%s is not a session directory", dir)
	}
	var newest time.Time
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == dir {
				return walkErr
			}
			return nil // an unreadable corner is not activity
		}
		if p == dir || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if mt := fi.ModTime(); mt.After(newest) {
			newest = mt
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return newest, nil
}
