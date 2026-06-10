package operations

import (
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/upgrade"
)

// Session operations wrap the harp-keyed session index so frontends never touch
// it directly (ADR 0019). The index is a thin filesystem-backed manager; these
// open it per call. Entries are returned as-is — a domain type a frontend may
// render, which 0019 permits; only the IO and decisions live here.

func openSessions() (*sessions.Manager, error) {
	return sessions.Open("")
}

// ListSessions returns every session index entry.
func ListSessions() ([]sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, err
	}
	idx, err := mgr.Load()
	if err != nil {
		return nil, err
	}
	return idx.Sessions, nil
}

// ListSessionsForProject returns the entries whose project dir matches,
// most-recent-first.
func ListSessionsForProject(projectDir string) ([]sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, err
	}
	return mgr.ListForProject(projectDir)
}

// PreviousSessionRef identifies a prior session to materialize: which backend
// produced it (agent-of-origin, enabling cross-agent handoff) and the
// agent-agnostic session id the owning agent server reassembles.
type PreviousSessionRef struct {
	SessionID string
	Backend   string
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
// (most-recent-first): skip the active harp's own entry and any entry not yet
// bound to a session id, then take the first prior one. Pure for testability.
func selectPreviousEntry(entries []sessions.Entry, activeHarp string) *PreviousSessionRef {
	for _, e := range entries {
		if e.HarpName == activeHarp || e.SessionID == "" {
			continue
		}
		return &PreviousSessionRef{SessionID: e.SessionID, Backend: e.Backend}
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

// AdoptRawSession mints a harp for a not-yet-indexed backend transcript and
// binds the transcript's session id / path to it, so the resume path can treat
// it like any other harp. The picker's adopt callback.
func AdoptRawSession(projectDir, backend, sessionID, transcriptPath string) (string, error) {
	mgr, err := openSessions()
	if err != nil {
		return "", err
	}
	entry, err := mgr.AssignHarp(projectDir, backend)
	if err != nil {
		return "", fmt.Errorf("assign harp: %w", err)
	}
	if err := mgr.BindSession(entry.HarpName, sessionID, transcriptPath); err != nil {
		return "", fmt.Errorf("bind session: %w", err)
	}
	return entry.HarpName, nil
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
	entry, _ := mgr.Find(harp)
	if entry == nil || entry.SessionID != "" {
		return nil
	}
	return mgr.BindSession(harp, sessionID, transcriptPath)
}
