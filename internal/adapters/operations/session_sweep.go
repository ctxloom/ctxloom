package operations

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// The session sweep: every ended session tidied BY RULE. Facts are gathered
// read-only (ClassifySessions), a pure table turns each session's facts into
// rows (DecideSweep), and only an apply acts on them (SweepSessions) — each
// action through the destroyer that already owns it (sessions.ReapSession,
// isolation.ReapWorktrees, PurgeSession), each of which re-checks the lock
// under its own hold. `clean`'s session half is this sweep with only the
// reclaim rows (SweepRequest.ReclaimOnly), so there is one table.

// SweepRequest is one sweep.
type SweepRequest struct {
	// ProjectDir scopes the sweep to the sessions recorded against it
	// (Entry.ProjectDir, matched exactly as `session list` matches it).
	ProjectDir string
	// ReclaimCutoff is the reclaim age bound. Required: zero refuses
	// (sessions.ErrNoAgeBound).
	ReclaimCutoff time.Time
	// PurgeCutoff is the purge age bound. It has NO default: zero reports
	// the purge rows held and never acts on them.
	PurgeCutoff time.Time
	// AllProjects widens the scope to every session under the home,
	// including directories whose record is missing.
	AllProjects bool
	// Apply is the plan/act switch. False changes nothing.
	Apply bool
	// ReclaimScope is the reclaim's widest Lifetime (sessions.ReapPolicy.Scope):
	// `clean --include-persist`.
	ReclaimScope paths.Lifetime
	// ReclaimOnly enables only the reclaim rows — `clean`'s view: aged
	// sessions holding something the reclaim takes, no purge, and no
	// worktree reaping below the reclaim age.
	ReclaimOnly bool
}

func (r SweepRequest) reapPolicy(apply bool) sessions.ReapPolicy {
	return sessions.ReapPolicy{Cutoff: r.ReclaimCutoff, Scope: r.ReclaimScope, Apply: apply}
}

// SessionFacts is everything the table decides one session by, gathered
// read-only.
type SessionFacts struct {
	Harp       string          `json:"harp"`
	Dir        string          `json:"dir"`
	ProjectDir string          `json:"project_dir,omitempty"`
	Origin     sessions.Origin `json:"origin,omitempty"`
	// LastActive is sessions.ActivityTime. ActivityErr is set instead when
	// it could not be read, and the session's age is then unknown.
	LastActive  time.Time `json:"last_active"`
	ActivityErr string    `json:"activity_error,omitempty"`
	// Lock is the liveness lock's verdict, read without holding it.
	Lock       sessionlock.Verdict `json:"-"`
	LockReason string              `json:"lock_reason,omitempty"`
	OwnerPID   int                 `json:"owner_pid,omitempty"`
	Kept       bool                `json:"kept,omitempty"`
	Distilled  bool                `json:"distilled"`
	// Worktrees is the scratch worktrees' classification, taken only for a
	// provably-dead session; WorktreeErr is set when it could not be taken.
	Worktrees   []isolation.WorktreeCandidate `json:"-"`
	WorktreeErr string                        `json:"worktree_error,omitempty"`
	// Reclaimable reports whether anything the reclaim takes is there, and
	// ReclaimBytes its size; ReclaimSymlink names a taken member that is
	// itself a symlink, which no reclaim follows.
	Reclaimable    bool   `json:"reclaimable,omitempty"`
	ReclaimBytes   int64  `json:"reclaim_bytes,omitempty"`
	ReclaimSymlink string `json:"reclaim_symlink,omitempty"`
	// Purgeable reports whether a purge would destroy anything (a
	// machine-written or derived file), and PurgeBytes its size.
	Purgeable  bool  `json:"purgeable,omitempty"`
	PurgeBytes int64 `json:"purge_bytes,omitempty"`
	// Mail counts the messages waiting in the spool's in/ and in/claimed/.
	Mail int `json:"mail,omitempty"`
}

// SweepAction is what one row does.
type SweepAction string

const (
	SweepSkip          SweepAction = "skip"
	SweepKeep          SweepAction = "keep"
	SweepReapWorktrees SweepAction = "reap-worktrees"
	SweepReclaim       SweepAction = "reclaim"
	SweepPurge         SweepAction = "purge"
	SweepSpare         SweepAction = "spare"
)

// SweepVerdict is where one row stands.
type SweepVerdict string

const (
	// SweepPlanned: an apply would act on it; nothing has changed.
	SweepPlanned SweepVerdict = "planned"
	// SweepDone: acted on.
	SweepDone SweepVerdict = "done"
	// SweepHeld: a purge with no purge age stated. Reported, never acted on.
	SweepHeld SweepVerdict = "held"
	// SweepLeft: left alone — by the table (skip, keep, spare), or by the
	// re-check an apply makes before acting.
	SweepLeft SweepVerdict = "left"
	// SweepFailed: the action was attempted and failed.
	SweepFailed SweepVerdict = "failed"
)

// SweepRow is one decision about one session.
type SweepRow struct {
	Harp    string       `json:"harp"`
	Action  SweepAction  `json:"action"`
	Verdict SweepVerdict `json:"verdict"`
	Bytes   int64        `json:"bytes,omitempty"`
	// Reason is why. Never empty for a row that leaves something alone.
	Reason string `json:"reason,omitempty"`
	// Command is the manual route when the sweep will not act itself.
	Command string `json:"command,omitempty"`
	// Worktrees are the checkouts a reap-worktrees row removes.
	Worktrees []string `json:"worktrees,omitempty"`
}

// SweepReport is one sweep: its rows, and a tally of their verdicts.
type SweepReport struct {
	Applied       bool                 `json:"applied"`
	ReclaimCutoff time.Time            `json:"reclaim_cutoff"`
	PurgeCutoff   time.Time            `json:"purge_cutoff"`
	Rows          []SweepRow           `json:"rows"`
	Counts        map[SweepVerdict]int `json:"counts"`
	// Untouched counts the sessions in scope the table had nothing to say
	// about.
	Untouched int `json:"untouched"`
	// Reclaim is the reclaim rows as the reaper's own report, filled for a
	// ReclaimOnly sweep (`clean`).
	Reclaim *sessions.Report `json:"reclaim,omitempty"`
}

// DecideSweep is the decision table. PURE: the facts and the request in,
// the rows out, applied in order.
func DecideSweep(f SessionFacts, req SweepRequest) []SweepRow {
	row := func(a SweepAction, v SweepVerdict, reason string) SweepRow {
		return SweepRow{Harp: f.Harp, Action: a, Verdict: v, Reason: reason}
	}
	aged := f.ActivityErr == "" && !req.ReclaimCutoff.IsZero() && !f.LastActive.After(req.ReclaimCutoff)
	if req.ReclaimOnly && (!f.Reclaimable || (f.ActivityErr == "" && !aged)) {
		return nil
	}

	switch {
	case f.Lock == sessionlock.Alive:
		return []SweepRow{row(SweepSkip, SweepLeft, "it is running: "+f.LockReason)}
	case f.Lock != sessionlock.Dead:
		r := row(SweepSkip, SweepLeft, "its liveness cannot be proven, so nothing is touched: "+f.LockReason)
		r.Command = fmt.Sprintf("ctxloom session purge %s --even-if-live", f.Harp)
		return []SweepRow{r}
	case f.ActivityErr != "":
		return []SweepRow{row(SweepSkip, SweepLeft, "its activity could not be read: "+f.ActivityErr)}
	case f.Kept:
		return []SweepRow{row(SweepKeep, SweepLeft, fmt.Sprintf("it carries a %s marker", paths.SessionKeepMarkerFileName))}
	case f.WorktreeErr != "":
		return []SweepRow{row(SweepSkip, SweepLeft, "its scratch worktrees could not be classified: "+f.WorktreeErr)}
	}

	var rows []SweepRow
	var clean []string
	var held []isolation.WorktreeCandidate
	for _, wt := range f.Worktrees {
		if wt.Verdict == isolation.VerdictReapable {
			clean = append(clean, wt.Path)
		} else {
			held = append(held, wt)
		}
	}
	if len(clean) > 0 {
		r := row(SweepReapWorktrees, SweepPlanned, "")
		r.Worktrees = clean
		rows = append(rows, r)
	}
	if len(held) > 0 {
		return append(rows, row(SweepSpare, SweepLeft, fmt.Sprintf(
			"its scratch worktree %s must be preserved (%s), so the session is spared from reclaim and purge and the work stays where it is",
			filepath.Base(held[0].Path), held[0].Reason)))
	}

	if aged && f.Reclaimable {
		r := row(SweepReclaim, SweepPlanned, "")
		r.Bytes = f.ReclaimBytes
		if f.ReclaimSymlink != "" {
			r.Verdict = SweepLeft
			r.Reason = fmt.Sprintf("its %s is a symlink, which a sweep never follows", f.ReclaimSymlink)
		}
		rows = append(rows, r)
	}
	if req.ReclaimOnly || !f.Purgeable {
		return rows
	}
	if !req.PurgeCutoff.IsZero() && f.LastActive.After(req.PurgeCutoff) {
		return rows
	}

	switch {
	case f.Mail > 0:
		rows = append(rows, row(SweepSpare, SweepLeft, fmt.Sprintf("%d undelivered message(s) wait in its spool, so it is spared from purge", f.Mail)))
	case !f.Distilled && f.Origin != sessions.OriginOneShot:
		r := row(SweepSpare, SweepLeft, "it was never distilled, so its transcript is its only record and it is never purged")
		r.Command = fmt.Sprintf("ctxloom session distill %s", f.Harp)
		rows = append(rows, r)
	case req.PurgeCutoff.IsZero():
		r := row(SweepPurge, SweepHeld, "no purge age is stated: pass --purge-older-than or set session_purge_age")
		r.Bytes = f.PurgeBytes
		rows = append(rows, r)
	default:
		r := row(SweepPurge, SweepPlanned, "")
		r.Bytes = f.PurgeBytes
		rows = append(rows, r)
	}
	return rows
}

// ClassifySessions gathers every in-scope session's facts. READ-ONLY: the
// lock is inspected, never held, and nothing on disk changes. Sessions are
// returned in directory order.
func ClassifySessions(ctx context.Context, g git.Git, req SweepRequest) ([]SessionFacts, error) {
	if g == nil {
		g = git.NewExec()
	}
	l, err := sessions.HomeLayout()
	if err != nil {
		return nil, fmt.Errorf("resolve sessions dir: %w", err)
	}
	store, err := openSessions()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(l.SessionsRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan %q: %w", l.SessionsRoot(), err)
	}
	var out []SessionFacts
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() || harp.Validate(e.Name()) != nil {
			continue
		}
		if f, ok := classifySession(ctx, g, l, store, e.Name(), req); ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// classifySession gathers one session's facts; false when it is out of the
// request's scope. A session whose record cannot be read has no project, so
// only an all-projects sweep sees it.
func classifySession(ctx context.Context, g git.Git, l sessions.Layout, store sessions.Store, name string, req SweepRequest) (SessionFacts, bool) {
	entry, _ := store.Find(name)
	if !req.AllProjects && (entry == nil || entry.ProjectDir != req.ProjectDir) {
		return SessionFacts{}, false
	}
	f := SessionFacts{Harp: name, Dir: l.Dir(name)}
	if entry != nil {
		f.ProjectDir, f.Origin = entry.ProjectDir, entry.Origin
	}
	if last, err := sessions.ActivityTime(l, name); err != nil {
		f.ActivityErr = err.Error()
	} else {
		f.LastActive = last
	}
	probe := sessionlock.Inspect(name)
	f.Lock, f.LockReason, f.OwnerPID = probe.Verdict, probe.Reason, probe.PID
	if _, err := os.Lstat(l.KeepMarker(name)); err == nil {
		f.Kept = true
	}
	f.Distilled = sessions.Distilled(f.Dir)

	members := req.reapPolicy(false).Members()
	if req.ReclaimScope == paths.Persist && !f.Distilled {
		members = sessions.ReapPolicy{}.Members() // ReapSession's own narrowing
	}
	f.Reclaimable, f.ReclaimBytes, f.ReclaimSymlink = measureReclaim(l, name, members)

	if probe.Verdict == sessionlock.Dead {
		wts, err := isolation.ClassifyHarpWorktrees(ctx, g, name, probe)
		if err != nil {
			f.WorktreeErr = err.Error()
		}
		f.Worktrees = wts
	}
	if req.ReclaimOnly {
		return f, true
	}
	f.Mail = countSpool(l, name)
	if entry != nil {
		if items, err := classifyHarpDir(f.Dir, entry); err == nil {
			for _, it := range items {
				if it.Class == PurgeClassMachine || it.Class == PurgeClassDerived {
					f.Purgeable = true
					f.PurgeBytes += it.Bytes
				}
			}
		}
	}
	return f, true
}

// measureReclaim reports whether any of members holds anything, their total
// size, and the first that is itself a symlink — the reaper's own measure
// (an absent member or an empty directory is nothing to reclaim).
func measureReclaim(l sessions.Layout, name string, members []paths.HarpMember) (populated bool, bytes int64, symlinked string) {
	for _, m := range members {
		p := l.Member(name, m)
		info, err := os.Lstat(p)
		switch {
		case err != nil:
			continue
		case info.Mode()&fs.ModeSymlink != 0:
			populated = true
			if symlinked == "" {
				symlinked = m.Rel()
			}
		case !info.IsDir():
			populated = true
			bytes += info.Size()
		default:
			_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, werr error) error {
				if werr != nil || q == p {
					return nil
				}
				populated = true
				if d.Type().IsRegular() {
					if fi, ierr := d.Info(); ierr == nil {
						bytes += fi.Size()
					}
				}
				return nil
			})
		}
	}
	return populated, bytes, symlinked
}

// countSpool counts the entries waiting in a session's spool in/ and
// in/claimed/. Every regular file counts, well-formed or not: a message
// nobody read is spared whatever its name.
func countSpool(l sessions.Layout, name string) int {
	n := 0
	for _, d := range []spool.Dir{spool.DirIn, spool.ClaimedDirName} {
		entries, err := os.ReadDir(filepath.Join(l.Spool(name), filepath.FromSlash(string(d))))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type().IsRegular() {
				n++
			}
		}
	}
	return n
}

// SweepSessions classifies, decides and — only when req.Apply — acts.
//
// An apply acts on exactly the rows this invocation planned, and re-checks
// each session before acting: its facts are gathered again and decided
// again, and a planned row the second decision no longer makes is left.
// Every action then goes through the destroyer that owns it, which takes
// the session's lock and refuses anything but a provably-dead owner.
func SweepSessions(ctx context.Context, g git.Git, req SweepRequest) (SweepReport, error) {
	if req.ReclaimCutoff.IsZero() {
		return SweepReport{}, sessions.ErrNoAgeBound
	}
	if g == nil {
		g = git.NewExec()
	}
	rep := SweepReport{Applied: req.Apply, ReclaimCutoff: req.ReclaimCutoff, PurgeCutoff: req.PurgeCutoff, Counts: map[SweepVerdict]int{}}
	if req.ReclaimOnly {
		rep.Reclaim = &sessions.Report{Applied: req.Apply, Cutoff: req.ReclaimCutoff, Members: req.reapPolicy(false).MemberRels()}
	}
	l, err := sessions.HomeLayout()
	if err != nil {
		return rep, fmt.Errorf("resolve sessions dir: %w", err)
	}
	facts, err := ClassifySessions(ctx, g, req)
	if err != nil {
		return rep, err
	}
	for _, f := range facts {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		rows := DecideSweep(f, req)
		if len(rows) == 0 {
			if req.ReclaimOnly && f.Reclaimable {
				rep.Reclaim.Newer++
			} else {
				rep.Untouched++
			}
			continue
		}
		var reaped *sessions.ReapCandidate
		if req.Apply {
			rows, reaped = applySweep(ctx, g, l, req, f, rows)
		}
		for _, r := range rows {
			rep.Rows = append(rep.Rows, r)
			rep.Counts[r.Verdict]++
		}
		if rep.Reclaim != nil {
			addReclaimCandidate(rep.Reclaim, f, rows, reaped)
		}
	}
	return rep, nil
}

// applySweep acts on one session's planned rows, after re-checking it. The
// candidate is the reaper's own verdict when a reclaim row ran.
func applySweep(ctx context.Context, g git.Git, l sessions.Layout, req SweepRequest, f SessionFacts, rows []SweepRow) ([]SweepRow, *sessions.ReapCandidate) {
	store, err := openSessions()
	if err != nil {
		return leaveAll(rows, "it could not be re-checked before acting: "+err.Error()), nil
	}
	fresh, ok := classifySession(ctx, g, l, store, f.Harp, req)
	if !ok {
		return leaveAll(rows, "it left the sweep's scope before it could be acted on"), nil
	}
	still := DecideSweep(fresh, req)
	for i := range rows {
		r := &rows[i]
		if r.Verdict == SweepPlanned && !slices.ContainsFunc(still, func(s SweepRow) bool { return s.Action == r.Action && s.Verdict == SweepPlanned }) {
			r.Verdict, r.Reason = SweepLeft, "re-checked before acting, it no longer qualifies"
		}
	}
	// THE RECLAIM RUNS FIRST. Its triage tears the clean worktrees down under
	// the reaper's own hold AFTER the reaper's age check; removing them first
	// would touch ephemeral/, and the age check would then read the session
	// as active and reclaim nothing.
	var reaped *sessions.ReapCandidate
	reclaimed := false
	for i := range rows {
		if r := &rows[i]; r.Action == SweepReclaim && r.Verdict == SweepPlanned {
			reaped = applyReclaim(ctx, g, l, req, r)
			reclaimed = r.Verdict == SweepDone
		}
	}
	for i := range rows {
		r := &rows[i]
		if r.Verdict != SweepPlanned {
			continue
		}
		switch r.Action {
		case SweepReapWorktrees:
			if reclaimed {
				r.Verdict, r.Reason = SweepDone, "torn down with the reclaim"
				continue
			}
			applyReapWorktrees(ctx, g, fresh, r)
		case SweepPurge:
			applyPurge(fresh, r)
		}
	}
	return rows, reaped
}

// leaveAll marks every planned row left, for why.
func leaveAll(rows []SweepRow, why string) []SweepRow {
	for i := range rows {
		if rows[i].Verdict == SweepPlanned {
			rows[i].Verdict, rows[i].Reason = SweepLeft, why
		}
	}
	return rows
}

// applyReapWorktrees removes exactly the planned checkouts, under the
// session's lock: isolation.ReapWorktrees re-checks each one and never
// forces, so a tree that went dirty since the plan is spared in place.
func applyReapWorktrees(ctx context.Context, g git.Git, f SessionFacts, r *SweepRow) {
	probe, release := sessionLocks{}.Acquire(f.Harp)
	defer release()
	if !probe.Dead {
		r.Verdict, r.Reason = SweepLeft, probe.Reason
		return
	}
	var planned []isolation.WorktreeCandidate
	for _, wt := range f.Worktrees {
		if wt.Verdict == isolation.VerdictReapable && slices.Contains(r.Worktrees, wt.Path) {
			planned = append(planned, wt)
		}
	}
	var removed []string
	for _, wt := range isolation.ReapWorktrees(ctx, g, planned) {
		if wt.Verdict == isolation.VerdictReaped {
			removed = append(removed, wt.Path)
		} else if r.Reason == "" {
			r.Reason = fmt.Sprintf("%s was left: %s", filepath.Base(wt.Path), wt.Reason)
		}
	}
	r.Worktrees = removed
	r.Verdict = SweepDone
	if len(removed) == 0 {
		r.Verdict = SweepLeft
	}
}

// applyReclaim is sessions.ReapSession with this adapter's triage.
func applyReclaim(ctx context.Context, g git.Git, l sessions.Layout, req SweepRequest, r *SweepRow) *sessions.ReapCandidate {
	c, ok := sessions.ReapSession(ctx, l, sessionLocks{}, r.Harp, req.reapPolicy(true), sessionTriage(g, isolation.ReapKeychainItems))
	if !ok {
		r.Verdict, r.Reason = SweepLeft, "nothing is left to reclaim"
		return nil
	}
	r.Bytes = c.Bytes
	switch c.Verdict {
	case sessions.ReapReclaimed:
		r.Verdict, r.Reason = SweepDone, c.Reason
	case "":
		r.Verdict, r.Reason = SweepLeft, "it has been active since the bound"
	default:
		r.Verdict, r.Reason = SweepLeft, c.Reason
	}
	return &c
}

// applyPurge is PurgeSession over both file populations. Only an internal
// one-shot is purged undistilled; PurgeSession refuses any other.
func applyPurge(f SessionFacts, r *SweepRow) {
	res, err := PurgeSession(r.Harp, PurgeSessionRequest{
		Populations: []PurgePopulation{PurgePopulationTranscript, PurgePopulationArtifacts},
		Undistilled: f.Origin == sessions.OriginOneShot,
		Apply:       true,
	})
	switch {
	case err == nil:
		r.Verdict, r.Bytes = SweepDone, res.BytesFreed
		for _, k := range res.Keep {
			if k.Class == PurgeClassAuthored {
				r.Reason = appendReason(r.Reason, "kept "+k.Rel)
			}
		}
	case errors.Is(err, ErrPurgeNothingToDo), errors.Is(err, ErrPurgeOwnerNotProvenDead), errors.Is(err, ErrPurgeUndistilled):
		r.Verdict, r.Reason = SweepLeft, err.Error()
	default:
		r.Verdict, r.Reason = SweepFailed, err.Error()
	}
}

func appendReason(reason, more string) string {
	if reason == "" {
		return more
	}
	return reason + "; " + more
}

// addReclaimCandidate folds one session's reclaim-relevant row into the
// reaper's report: the reaper's own candidate when a reclaim ran, else one
// built from the facts.
func addReclaimCandidate(rep *sessions.Report, f SessionFacts, rows []SweepRow, reaped *sessions.ReapCandidate) {
	c := sessions.ReapCandidate{Harp: f.Harp, Dir: f.Dir, LastActive: f.LastActive, Bytes: f.ReclaimBytes, OwnerPID: f.OwnerPID}
	found := false
	for _, r := range rows {
		switch r.Action {
		case SweepSkip:
			c.Verdict, c.Reason = sessions.ReapSkipped, r.Reason
		case SweepKeep:
			c.Verdict, c.Reason = sessions.ReapKept, r.Reason
		case SweepSpare:
			c.Verdict, c.Reason = sessions.ReapSpared, r.Reason
		case SweepReclaim:
			switch {
			case reaped != nil:
				c = *reaped
			case r.Verdict == SweepPlanned:
				c.Verdict = sessions.ReapReclaimable
			default:
				c.Verdict, c.Reason = sessions.ReapSkipped, r.Reason
			}
		default:
			continue
		}
		found = true
		break
	}
	if !found || c.Verdict == "" {
		return
	}
	switch c.Verdict {
	case sessions.ReapReclaimed:
		rep.Reclaimed++
		rep.Bytes += c.Bytes
	case sessions.ReapReclaimable:
		rep.Bytes += c.Bytes
	case sessions.ReapSpared:
		rep.Spared++
	case sessions.ReapKept:
		rep.Kept++
	default:
		rep.Skipped++
	}
	rep.Candidates = append(rep.Candidates, c)
}
