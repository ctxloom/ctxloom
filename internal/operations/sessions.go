package operations

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/ctxloom/ctxloom/internal/engineversion"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
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
// ctxloom://sessions resource). ListSessions (unsorted) stays for
// order-insensitive callers that only need membership.
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
//
// Resolves through the store's lineage-aware lookup (sessions.Store.
// FindBySessionID), so an id a /clear has since rotated PAST — no longer any
// entry's CURRENT SessionID, but still recorded in that entry's Rotations —
// still resolves to the harp that owns it. A plain linear scan over
// ListSessions (this function's previous body) can only ever match the live
// binding, so a caller handed a pre-clear session id — a stale hook payload,
// an old vendor transcript, `memory show`'s own mtime fallback — found no
// harp at all.
func HarpForSession(sessionID string) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	mgr, err := openSessions()
	if err != nil {
		return "", err
	}
	entry, err := mgr.FindBySessionID(sessionID)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "", nil
	}
	return entry.HarpName, nil
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

// probeEngineVersion is the seam AssignSession probes through. A package var
// so tests can drive both outcomes without an engine binary installed — the
// production value execs the real CLI through backends' shared cached prober.
var probeEngineVersion = backends.ProbeEngineVersion

// AssignSession mints a fresh harp for a new run in projectDir under backend,
// records the engine's version against it, and returns the pending index entry
// (SessionID unbound until the spawned LLM initializes). The pre-launch naming
// step in `ctxloom run` and in the ACP session opener.
//
// It is the composition of its two halves, AssignSessionHarp and
// RecordSessionEngineVersion, for the callers that want both and can afford
// to wait for both. A caller that must publish the harp somewhere BEFORE the
// probe — the delegation spawn path, where the probe used to sit between
// minting the child's address and registering the child anywhere an operator
// could see it (task affected-yearly) — calls the two halves itself, in that
// order.
//
// ctx bounds the probe. It used to be discarded outright in favour of
// context.Background(), which meant a wedged `--version` had no deadline at
// all on any path.
func AssignSession(ctx context.Context, projectDir, backend string) (sessions.Entry, error) {
	entry, err := AssignSessionHarp(projectDir, backend)
	if err != nil {
		return sessions.Entry{}, err
	}
	if version, ok := RecordSessionEngineVersion(ctx, entry.HarpName, backend); ok {
		entry.EngineVersion = version
	}
	return entry, nil
}

// AssignSessionHarp mints the harp alone: the session's address, written to
// the index, with nothing else consulted.
//
// It is deliberately the CHEAP half. Everything that can block for an
// unbounded time on the session-start path — an exec of a vendor CLI above
// all — belongs in RecordSessionEngineVersion, so that a caller whose next
// act is to publish the harp can do that first and let the slow, optional
// enrichment follow.
func AssignSessionHarp(projectDir, backend string) (sessions.Entry, error) {
	mgr, err := openSessions()
	if err != nil {
		return sessions.Entry{}, err
	}
	return mgr.AssignHarp(projectDir, backend)
}

// RecordSessionEngineVersion probes backend's installed CLI and records what
// it says against harp, reporting the recorded version and whether anything
// was recorded.
//
// WHY AT SESSION START (task wrought-spearman): this is the only moment at
// which "what is installed" and "what will write this session's transcript"
// are the same fact. Read it back later and a probe would answer for whatever
// is installed THEN — see sessions.Entry.EngineVersion.
//
// A failed probe does NOT fail the session. The two failure levels are
// different costs: refusing to START a session because an engine could not be
// asked its version would deny a user their working engine over a diagnostic,
// while reading a stored transcript with no idea what wrote it silently
// returns a plausible-but-wrong conversation. So this warns and records
// nothing, and the refusal lands where it actually protects something — the
// read path, which treats an unrecorded version as unknown and stops.
//
// ctx bounds the probe on top of the prober's own budget
// (engineversion.DefaultProbeTimeout); neither is optional, because this is
// the step that exec's somebody else's binary.
func RecordSessionEngineVersion(ctx context.Context, harp, backend string) (string, bool) {
	version, perr := probeEngineVersion(ctx, backend)
	if perr != nil {
		// An engine that declares no version command at all (mock, the
		// generic acp backend — neither has one binary whose version would
		// mean anything) is not a problem to report: it is a stated fact about
		// that backend, and warning on every run would train users to ignore
		// the warning that matters.
		var undeclared *engineversion.NoVersionCommandError
		if !errors.As(perr, &undeclared) {
			clidiag.Warn("ctxloom", "engine version: %v — this session's transcript will not be readable back, "+
				"because ctxloom will not guess which format wrote it", perr)
		}
		return "", false
	}
	mgr, err := openSessions()
	if err != nil {
		clidiag.Warn("ctxloom", "engine version: record %s for %s: %v", version, harp, err)
		return "", false
	}
	if rerr := mgr.RecordEngineVersion(harp, version); rerr != nil {
		clidiag.Warn("ctxloom", "engine version: record %s for %s: %v", version, harp, rerr)
		return "", false
	}
	return version, true
}

// EndSession ends harp's session: it stamps the entry's end time AND removes
// the session's per-session engine-home instance from the project tree.
// Idempotent; the end stamp lets the time-window transcript fallback find the
// session even when the bind hook never fired.
//
// NAMED FOR BOTH HALVES, deliberately — it used to be MarkSessionEnded, which
// described only the index write. A caller reaching for a session-end helper
// must see that it also DELETES a directory, for the same reason
// WriteAndRecordSyncSummary is not called WriteSyncSummary: a destructive
// second effect hiding behind a read-ish name is how it gets called from
// somewhere that did not want it.
//
// THE ORDER IS LOAD → MARK → REMOVE, and each step depends on the one before:
//
//   - LOAD first, because the instance lives under the session's own PROJECT
//     tree and the index entry is the only record of which project that is.
//     A harp the index does not carry resolves to no project and therefore to
//     no removal — guessing would make this a project-wide delete keyed by an
//     unvalidated string.
//   - MARK before REMOVE, so that a removal which fails still leaves an entry
//     the startup sweep (ReapOrphanedSessionHomes) reads as ended and can
//     collect later. Removing first and failing to mark would strand the
//     leftover as "live" forever.
//
// The removal is best-effort and never fails the session end: see
// removeSessionInstance.
func EndSession(harp string, at time.Time) error {
	mgr, err := openSessions()
	if err != nil {
		return err
	}
	// Read the project BEFORE the mark: nothing here mutates it, but the entry
	// is what names the tree the instance sits in, and a failed mark must not
	// cost us the ability to clean up.
	entry, ferr := mgr.Find(harp)
	if ferr != nil {
		clidiag.Warn("ctxloom", "session end: cannot read %s's index entry, so its engine-home instance is left for the startup sweep: %v", harp, ferr)
	}
	if err := mgr.MarkEnded(harp, at); err != nil {
		return err
	}
	if entry != nil {
		removeSessionInstance(entry.ProjectDir, harp)
	}
	return nil
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

// BindSession records the backend session_id and transcript path for harp.
// A harp absent from the index or an empty id is a no-op — the SessionStart
// bind hook must never fail the host backend (CLAUDE.md fault tolerance).
//
// An ALREADY-BOUND entry is deliberately not short-circuited here. The hook
// fires again when an engine rotates its transcript under a live process
// (claude-code's /clear), and swallowing that call at this layer is what kept
// a harp pinned to the first transcript it ever had. sessions.Manager
// .BindSession makes the same-vs-rotated decision under the index lock, where
// it cannot race a concurrent binder.
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
		// `session transcript watch`/resume fails with "no session bound". The
		// SessionStart hook must still never fail the host backend (CLAUDE.md
		// fault tolerance), so this warns rather than returning the error.
		clidiag.Warn("ctxloom", "SessionStart: bind %s: read session index: %v (session id not recorded)", harp, ferr)
		return nil
	}
	if entry == nil {
		return nil
	}
	return mgr.BindSession(harp, sessionID, transcriptPath)
}
