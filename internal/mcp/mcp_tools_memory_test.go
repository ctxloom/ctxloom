package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// These tests pin the identity-first session-selection logic:
// recover_session and get_previous_session must prefer the
// harp->SessionID binding recorded in the session index over an mtime-position
// pick from the backend's own transcript listing. A backend (Claude Code and
// others) rewrites/touches a transcript file's mtime when a session is
// resumed, so "newest by mtime" is not reliably "the session that just
// ended" — the identity binding, set once at session-start and never touched
// again, is exact.

// alwaysExists and neverExists are transcriptExists fakes for
// recoverTargetSessionID tests below.
func alwaysExists(string) bool { return true }
func neverExists(string) bool  { return false }

func TestRecoverTargetSessionID_PrefersIdentityOverMtime(t *testing.T) {
	// The active harp is bound to "correct-session", but the mtime-based
	// listing reports a DIFFERENT, newer-touched session first (simulating a
	// resumed/stale transcript out-ranking the real current session by mtime).
	activeEntry := &sessions.Entry{HarpName: "active-harp", SessionID: "correct-session", Backend: "claude-code"}
	mtimeSessions := []agent.SessionMeta{
		{ID: "stale-resumed-session"}, // newest by mtime, but NOT the active session
		{ID: "correct-session"},
	}

	got := recoverTargetSessionID(activeEntry, "claude-code", nil, alwaysExists)
	assert.Equal(t, "correct-session", got, "identity-only probe must resolve without consulting the mtime listing")

	// Even if the mtime listing were passed too, identity still wins.
	got = recoverTargetSessionID(activeEntry, "claude-code", mtimeSessions, alwaysExists)
	assert.Equal(t, "correct-session", got)
}

func TestRecoverTargetSessionID_FallsBackToMtimeWhenUnbound(t *testing.T) {
	mtimeSessions := []agent.SessionMeta{{ID: "newest-by-mtime"}, {ID: "older"}}

	// No entry at all (harp not in the index).
	assert.Equal(t, "newest-by-mtime", recoverTargetSessionID(nil, "claude-code", mtimeSessions, alwaysExists))

	// Entry present but never bound (SessionStart hook never fired).
	unbound := &sessions.Entry{HarpName: "active-harp", Backend: "claude-code"}
	assert.Equal(t, "newest-by-mtime", recoverTargetSessionID(unbound, "claude-code", mtimeSessions, alwaysExists))

	// Empty inputs resolve to "" (caller decides how to report "no sessions").
	assert.Equal(t, "", recoverTargetSessionID(nil, "claude-code", nil, alwaysExists))
}

func TestRecoverTargetSessionID_BackendMismatchFallsBackToMtime(t *testing.T) {
	// The bound entry belongs to a DIFFERENT backend than the one being read
	// from — using its session id would look up the wrong backend's store.
	entry := &sessions.Entry{HarpName: "active-harp", SessionID: "codex-session", Backend: "codex"}
	mtimeSessions := []agent.SessionMeta{{ID: "claude-newest"}}

	got := recoverTargetSessionID(entry, "claude-code", mtimeSessions, alwaysExists)
	assert.Equal(t, "claude-newest", got)
}

// TestRecoverTargetSessionID_StaleBindingFallsBackToMtime is a
// regression test: the harp is bound to a session id, but its recorded
// transcript file no longer exists (rotated/deleted — a stale index entry).
// Trusting the dead id would skip the mtime listing entirely and hand the
// caller an id that can't be loaded; identity must instead degrade to the
// mtime-based pick, same as an unbound harp.
func TestRecoverTargetSessionID_StaleBindingFallsBackToMtime(t *testing.T) {
	staleEntry := &sessions.Entry{
		HarpName:       "active-harp",
		SessionID:      "dead-session",
		Backend:        "claude-code",
		TranscriptPath: "/gone/dead-session.jsonl",
	}
	mtimeSessions := []agent.SessionMeta{{ID: "newest-existing"}, {ID: "older"}}

	got := recoverTargetSessionID(staleEntry, "claude-code", mtimeSessions, neverExists)
	assert.Equal(t, "newest-existing", got, "a bound id whose transcript is gone must fall back to the newest EXISTING transcript")

	// identity-only probe (no mtime listing yet): must report "" so the
	// caller knows to go fetch one, rather than trusting the dead id.
	got = recoverTargetSessionID(staleEntry, "claude-code", nil, neverExists)
	assert.Equal(t, "", got, "identity-only probe of a stale binding must not return the dead id")
}

// TestRecoverTargetSessionID_NoTranscriptPathTrustsIdentity covers an entry
// that was bound but never had its TranscriptPath recorded (older index
// entries, or a backend that doesn't report one): identity is trusted as
// before rather than being forced through the existence check, since "" is
// not a claim that can be falsified.
func TestRecoverTargetSessionID_NoTranscriptPathTrustsIdentity(t *testing.T) {
	entry := &sessions.Entry{HarpName: "active-harp", SessionID: "correct-session", Backend: "claude-code"}
	got := recoverTargetSessionID(entry, "claude-code", nil, neverExists)
	assert.Equal(t, "correct-session", got)
}

func TestPreviousSessionFromMtime_ExcludesActiveSessionRegardlessOfPosition(t *testing.T) {
	// Reproduces the root cause directly: an OLDER, already-ended
	// session was touched again (e.g. resumed) and now has a NEWER mtime than
	// the still-active session, so the active session is NOT positionally
	// first. A blind metas[1] pick (the old behavior) would return the active
	// session's own id as "previous" — nonsensical. Identity-aware exclusion
	// must skip whatever position the active session sits at and return the
	// genuinely-older session instead.
	metas := []agent.SessionMeta{
		{ID: "touched-old-session"}, // older session, but touched again -> newest mtime
		{ID: "active-session"},      // the caller's own current session, older mtime
	}

	got := previousSessionFromMtime("active-session", metas)
	assert.Equal(t, "touched-old-session", got, "must skip the active session wherever it sits, not assume position 0/blindly take position 1")
}

func TestPreviousSessionFromMtime_TakesNewestNonActiveAmongForeignNoise(t *testing.T) {
	// The function's contract is "exclude the KNOWN active session, then take
	// newest of the rest" — a best-effort last resort, not omniscient: it
	// cannot distinguish a genuinely-previous session from an unrelated
	// foreign transcript that happens to be newer. This pins that contract so
	// a future change doesn't accidentally promise more than mtime data can
	// deliver.
	metas := []agent.SessionMeta{
		{ID: "resumed-elsewhere"}, // newest by mtime, unrelated to this project's flow
		{ID: "active-session"},
		{ID: "actual-previous"},
	}

	got := previousSessionFromMtime("active-session", metas)
	assert.Equal(t, "resumed-elsewhere", got)
}

// TestPreviousSessionFromMtime_NoActiveKnownTakesSecondNewest reproduces a
// defect: when activeSessionID is unknown ("" — the current harp
// is unbound), metas[0] is the ACTIVE session (still live, being written by
// the running process), not "previous". Returning it would hand the caller
// their own live session as if it were an earlier one. Since the active
// session can't be identified by id here, assume it sits at position 0 (the
// newest by mtime, which is exactly how a live session's transcript ranks)
// and return the second-newest instead.
func TestPreviousSessionFromMtime_NoActiveKnownTakesSecondNewest(t *testing.T) {
	metas := []agent.SessionMeta{{ID: "newest"}, {ID: "second-newest"}, {ID: "older"}}
	assert.Equal(t, "second-newest", previousSessionFromMtime("", metas))
}

// TestPreviousSessionFromMtime_NoActiveKnownAndOnlyOneSession covers the
// degenerate case: with no active id known and only one transcript in the
// store, there is no second-newest to fall back to — must return "", never
// the sole (presumed-active) entry.
func TestPreviousSessionFromMtime_NoActiveKnownAndOnlyOneSession(t *testing.T) {
	assert.Equal(t, "", previousSessionFromMtime("", []agent.SessionMeta{{ID: "only"}}))
}

func TestPreviousSessionFromMtime_EmptyWhenNothingRemains(t *testing.T) {
	assert.Equal(t, "", previousSessionFromMtime("only-session", []agent.SessionMeta{{ID: "only-session"}}))
	assert.Equal(t, "", previousSessionFromMtime("", nil))
}
