package operations

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// Session operations wrap the harp-keyed session index so frontends never touch
// it directly (ADR 0019). The index is a thin filesystem-backed manager; these
// open it per call. Entries are returned as-is — a domain type a frontend may
// render, which 0019 permits; only the IO and decisions live here.

func openSessions() (sessions.Store, error) {
	return sessions.Open("")
}

// isUnrecoverable reports whether a session index entry can never be acted on
// again, so listing should silently drop it (and forget the dangling row): its
// transcript was bound but the file is now gone, AND it has no distilled essence
// to fall back on. A still-pending entry (no transcript bound yet — a run in
// flight) and a distilled entry are recoverable, so both are kept.
func isUnrecoverable(e sessions.Entry) bool {
	if e.PurgedAt != nil {
		return false // purged on purpose: the row IS the record now, checked
		// first so a purge that already destroyed the transcript this
		// predicate would otherwise flag can never race its own index write —
		// see operations.PurgeSession and sessions.Manager.MarkPurged's
		// mark-before-destroy ordering.
	}
	if e.Summary != "" || len(e.Detail) > 0 {
		return false // distilled: essence.md is still viewable
	}
	// ctxloom's OWN capture (transcript.jsonl) is a full fallback for a
	// vendor transcript the vendor has since pruned — the whole point of
	// capturing runner-side. Missing this check made the reap delete
	// perfectly recoverable sessions. Manager.Reconcile fills this
	// computed-on-read field on the entry it judges; without that fill the
	// check here can never fire, which is why the fix had two halves.
	if e.CanonicalTranscriptPath != "" {
		return false
	}
	if e.TranscriptPath == "" {
		return false // pending/unbound: the session is still in progress
	}
	return transcriptGone(e.TranscriptPath)
}

// transcriptGone reports whether the transcript file is genuinely absent
// (ENOENT). Any other os.Stat error — permission denied, a transient I/O hiccup
// on a network mount, EINTR, a temporarily-unavailable parent dir — is treated
// as "not gone" so a degraded environment never permanently forgets a still-
// recoverable session (CLAUDE.md fault tolerance: tolerate transient failures,
// never destructive action). Only true non-existence makes a bound transcript
// unrecoverable.
func transcriptGone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// ListSessions returns every session index entry, after reconciling away any
// that have become unrecoverable (see isUnrecoverable) so a dead pointer never
// reaches a frontend.
func ListSessions() ([]sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, err
	}
	return mgr.Reconcile(isUnrecoverable)
}

// ListSessionsForProject returns the entries whose project dir matches,
// most-recent-first, after reconciling the index so unrecoverable sessions are
// silently dropped here too (the resume picker and the VSCode companion both
// arrive through this path).
func ListSessionsForProject(projectDir string) ([]sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, err
	}
	if _, err := mgr.Reconcile(isUnrecoverable); err != nil {
		return nil, err
	}
	return mgr.ListForProject(projectDir)
}

// ListAllSessions returns every recorded session across all projects,
// most-recent-first by last-worked time (ActivityTime), after reconciling the
// index so unrecoverable sessions are dropped here too. Mirrors
// ListSessionsForProject without the project filter — the ordered all-projects
// listing (`session list --all`, the list_sessions MCP tool, the
// ctxloom://sessions resource). ListSessions (unsorted) stays for the
// order-insensitive callers that only need membership (HarpForSession).
func ListAllSessions() ([]sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, err
	}
	if _, err := mgr.Reconcile(isUnrecoverable); err != nil {
		return nil, err
	}
	return mgr.ListAll()
}

// PreviousSessionRef identifies a prior session to materialize: which backend
// produced it (agent-of-origin, enabling cross-agent handoff) and the
// agent-agnostic session id the owning agent server reassembles. For an
// ACP-launched session there is no backend session id — SessionID is empty and
// Harp is the sole materialization key (its own canonical transcript lives at
// ~/.ctxloom/sessions/<harp>/), so the caller distills by harp instead of
// asking a backend store to reassemble an id it never issued.
type PreviousSessionRef struct {
	SessionID string
	Backend   string
	Harp      string
}

// ResolvePreviousSession resolves the session before the active harp's, for a
// project, from the session index — the authority for ordering, agent-of-origin,
// and cross-agent routing (ADR 0019). Entries come most-recent-first; the active
// harp's own entry is skipped and the first prior entry that carries a bound
// session id wins. Returns nil (no error) when the index has no such entry, so
// the caller can fall back to the owning agent's own store listing.
//
// This replaces the per-backend GetPreviousSession readers: ctxloom decides
// WHICH session is previous (and which agent owns it); the agent server only
// materializes a given id. This also keeps agent modules from
// importing internal/sessions.
func ResolvePreviousSession(projectDir, activeHarp string) (*PreviousSessionRef, error) {
	entries, err := ListSessionsForProject(projectDir)
	if err != nil {
		return nil, err
	}
	return selectPreviousEntry(entries, activeHarp), nil
}

// selectPreviousEntry picks the previous session from project entries
// (most-recent-first): skip the active harp's own entry and any entry that is
// not yet materializable, then take the first prior one. An entry is
// materializable once it has EITHER a bound backend SessionID (the owning agent
// store can reassemble it) OR a canonical transcript on disk (an ACP session
// distillable by harp). A still-pending entry — no session id and no canonical
// transcript — is skipped. Pure for testability.
func selectPreviousEntry(entries []sessions.Entry, activeHarp string) *PreviousSessionRef {
	for _, e := range entries {
		if e.HarpName == activeHarp {
			continue
		}
		if e.SessionID == "" && e.CanonicalTranscriptPath == "" {
			continue
		}
		return &PreviousSessionRef{SessionID: e.SessionID, Backend: e.Backend, Harp: e.HarpName}
	}
	return nil
}

// HarpForSession returns the harp that owns the given backend session id, or ""
// if the index has no bound entry for it. Used to key a distilled session's plan
// files (stored under ~/.ctxloom/sessions/<harp>/) when distilling a session by
// id — e.g. a previous or cross-agent session, where the active harp is wrong.
func HarpForSession(sessionID string) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	entries, err := ListSessions()
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.SessionID == sessionID {
			return e.HarpName, nil
		}
	}
	return "", nil
}

// GetSession returns the entry for harp, or nil if absent.
func GetSession(harp string) (*sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, err
	}
	return mgr.Find(harp)
}

// RenameSession renames a harp entry; the backend transcript is unaffected.
// The new name is validated by sessions.Manager.Rename (harp.Validate), not
// here — a harp name is a filesystem path component, so the refusal belongs
// where the data is, guarding every caller rather than this one.
func RenameSession(oldName, newName string) error {
	mgr, err := openSessions()
	if err != nil {
		return err
	}
	return mgr.Rename(oldName, newName)
}

// ForgetSession drops a harp entry from the index, leaving transcript and
// essence files on disk.
func ForgetSession(harp string) error {
	mgr, err := openSessions()
	if err != nil {
		return err
	}
	return mgr.Forget(harp)
}

// AssignSession mints a fresh harp for a new run in projectDir under backend
// and returns the pending index entry (SessionID unbound until the spawned LLM
// initializes). The pre-launch naming step in `ctxloom run`.
func AssignSession(projectDir, backend string) (sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return sessions.Entry{}, err
	}
	return mgr.AssignHarp(projectDir, backend)
}

// MarkSessionEnded stamps the harp entry's end time. Idempotent; lets the
// time-window transcript fallback find the session even when the bind hook
// never fired.
func MarkSessionEnded(harp string, at time.Time) error {
	mgr, err := openSessions()
	if err != nil {
		return err
	}
	return mgr.MarkEnded(harp, at)
}

// SessionIndexUpgrade reports whether loading the index would apply an in-memory
// schema upgrade, returning the pending upgrade and a commit closure bound to
// the loaded manager (nil pending when the on-disk index is already current).
// The interactive caller prompts before invoking commit; the index is never
// rewritten silently. See cmd's confirmUpgrade.
func SessionIndexUpgrade() (*upgrade.Pending, func() error, error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, nil, err
	}
	if _, err := mgr.Load(); err != nil {
		return nil, nil, err
	}
	return mgr.PendingUpgrade(), mgr.CommitUpgrade, nil
}

// BindSession records the backend session_id and transcript path for harp,
// first-bind-wins. A harp absent from the index, an empty id, or an
// already-bound entry is a no-op — the SessionStart bind hook must never fail
// the host backend (CLAUDE.md fault tolerance).
func BindSession(harp, sessionID, transcriptPath string) error {
	if harp == "" || sessionID == "" {
		return nil
	}
	mgr, err := openSessions()
	if err != nil {
		return err
	}
	entry, ferr := mgr.Find(harp)
	if ferr != nil {
		// A transient index-read failure used to be indistinguishable from
		// "no entry for this harp" — both took the silent no-op below.
		// First-bind-wins never retries, so a harp that misses its bind here
		// has no session id for the rest of its life, and every later
		// `session watch`/resume fails with "no session bound". The
		// SessionStart hook must still never fail the host backend (CLAUDE.md
		// fault tolerance), so this warns rather than returning the error.
		clidiag.Warn("ctxloom", "SessionStart: bind %s: read session index: %v (session id not recorded)", harp, ferr)
		return nil
	}
	if entry == nil || entry.SessionID != "" {
		return nil
	}
	return mgr.BindSession(harp, sessionID, transcriptPath)
}
