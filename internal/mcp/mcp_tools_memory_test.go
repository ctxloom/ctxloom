package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
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

// noOwner is the OwnerHarp fake for a candidate the attribution source cannot
// place. ownedBy builds one that claims a specific harp.
func noOwner(string) string { return "" }

func ownedBy(harp string) func(string) string { return func(string) string { return harp } }

func lineage(ids ...string) []operations.HarpTranscript {
	out := make([]operations.HarpTranscript, 0, len(ids))
	for _, id := range ids {
		out = append(out, operations.HarpTranscript{SessionID: id})
	}
	return out
}

// The harp is the only identity a /clear does not rotate, so the harp's own
// lineage is what recover resolves against. The current session is skipped
// because its content is already in the context window — "recovering" it
// restores nothing, which is the failure this ordering exists to prevent.
func TestRecoverResolution_PrefersLineagePredecessorOverCurrent(t *testing.T) {
	r := recoverResolution{
		Lineage:    lineage("post-clear-current", "pre-clear-work", "older-still"),
		CurrentID:  "post-clear-current",
		ActiveHarp: "active-harp",
		OwnerHarp:  noOwner,
	}
	assert.Equal(t, "pre-clear-work", r.target(),
		"the newest lineage entry that is NOT the current session must win")
}

// No predecessor means there is genuinely nothing to recover. It must REFUSE
// rather than fall through to the current session.
func TestRecoverResolution_RefusesWhenLineageHoldsOnlyCurrent(t *testing.T) {
	r := recoverResolution{
		Lineage:    lineage("only-current"),
		CurrentID:  "only-current",
		ActiveHarp: "active-harp",
		OwnerHarp:  noOwner,
	}
	assert.Equal(t, "", r.target(),
		"a lineage holding only the current session must refuse, never return it")
}

// The positional listing is a last resort for a harp with NO lineage; it must
// never outrank the harp's own history.
func TestRecoverResolution_LineageOutranksPositionalListing(t *testing.T) {
	r := recoverResolution{
		Lineage:       lineage("harps-own-predecessor"),
		MtimeSessions: []agent.SessionMeta{{ID: "newest-touched-elsewhere"}},
		ActiveHarp:    "active-harp",
		OwnerHarp:     noOwner,
	}
	assert.Equal(t, "harps-own-predecessor", r.target())
}

// Recover promises the CURRENT session's history. A candidate provably owned by
// another harp is a wrong answer the caller cannot distinguish from a right one.
func TestRecoverResolution_RejectsCrossHarpCandidate(t *testing.T) {
	r := recoverResolution{
		MtimeSessions: []agent.SessionMeta{{ID: "foreign-session"}},
		ActiveHarp:    "active-harp",
		OwnerHarp:     ownedBy("some-other-harp"),
	}
	assert.Equal(t, "", r.target(), "a candidate owned by another harp must be refused")

	r.OwnerHarp = ownedBy("active-harp")
	assert.Equal(t, "foreign-session", r.target(), "the active harp's own transcript still resolves")
}

// Rejecting on DOUBT would strand every harp the attribution source never
// recorded, so an unattributable candidate is permitted.
func TestRecoverResolution_UnattributableCandidateStillResolves(t *testing.T) {
	r := recoverResolution{
		MtimeSessions: []agent.SessionMeta{{ID: "unknown-owner"}},
		ActiveHarp:    "active-harp",
		OwnerHarp:     noOwner,
	}
	assert.Equal(t, "unknown-owner", r.target())

	r.ActiveHarp = ""
	r.OwnerHarp = ownedBy("some-other-harp")
	assert.Equal(t, "unknown-owner", r.target(), "no active harp means no cross-harp claim can be made")
}

func TestRecoverResolution_RefusesWhenNothingAtAll(t *testing.T) {
	r := recoverResolution{ActiveHarp: "active-harp", OwnerHarp: noOwner}
	assert.Equal(t, "", r.target())
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
