package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRecordedSessionEntries_UnknownHarpErrorsRatherThanPanicking pins the
// UNRESOLVABLE half of the resume contract.
//
// GetSession documents "returns the entry for harp, or nil if absent" and
// returns (nil, nil) for an absent harp; RecordedSessionEntries checked only
// the error before dereferencing entry.SessionID, so `ctxloom run --session
// <typo>` died on a nil pointer. A harp is three random words — mistyping one
// is the single most ordinary error available on this command, and it took the
// process down with a stack trace.
//
// resumeFullContext's own documented contract ("an unresolvable or unbound harp
// warns and returns existing unchanged; a typo'd or stale --session must never
// block the launch") was only honoured for the UNBOUND half. This is the other
// half, asserted at the shared primitive rather than at one caller, because the
// same call also serves the ACP resume path (engine_session.go) and the
// coordinator's ended-child resume (coord/spawner.go ResumeContext).
//
// NotPanics, not a bare call: an unrecovered panic here would take the whole
// test binary down and report as an unrelated package failure, which is a worse
// signal than a named assertion.
func TestRecordedSessionEntries_UnknownHarpErrorsRatherThanPanicking(t *testing.T) {
	testsupport.ProjectDir(t) // isolated HOME: the session index must be empty

	var entries []agent.SessionEntry
	var err error
	require.NotPanics(t, func() {
		entries, err = RecordedSessionEntries(context.Background(), "no-such-harp-anywhere")
	}, "a harp absent from the session index must not nil-deref")

	require.Error(t, err, "an unknown harp is an error, not a silent empty resume")
	assert.Nil(t, entries)
	assert.Contains(t, err.Error(), "no-such-harp-anywhere",
		"the error must name the harp the user typed — that is the whole diagnostic")
	assert.Contains(t, err.Error(), "session list",
		"it must say where to find the real harps; three random words are not guessable")
}

// TestRecordedSessionEntries_PrefersCanonicalTranscriptOverBackend pins the
// J001200 "archive and the archive" fix: RecordedSessionEntries used to resolve a
// harp's entries by asking the BACKEND's own session reader for
// entry.SessionID, never reading the canonical transcript ctxloom itself
// captured, converted and schema-versions. That meant a session whose vendor
// store had rotated, aged out, or simply belonged to a mock/test backend
// (this fixture) could not be resumed from ctxloom's own copy of it — exactly
// the case `session backfill` exists to rescue a conversation FROM.
//
// seedCanonicalFeedHarp deliberately leaves SessionID unbound, so this test
// only passes if RecordedSessionEntries never needs the backend reader at
// all: it must resolve entirely from entry.CanonicalTranscriptPath.
func TestRecordedSessionEntries_PrefersCanonicalTranscriptOverBackend(t *testing.T) {
	testsupport.Isolate(t)
	entry := seedCanonicalFeedHarp(t, "J001200-RESUME-REGRESSION-CANONICAL-PAYLOAD")

	entries, err := RecordedSessionEntries(context.Background(), entry.HarpName)
	require.NoError(t, err, "a canonical-backed harp must resolve without ever touching the (unbound) backend reader")
	require.Len(t, entries, 1)
	assert.Equal(t, "J001200-RESUME-REGRESSION-CANONICAL-PAYLOAD", entries[0].Content)
}

// TestRenderResumedTranscript_EmptyEntriesWarns is a regression guard: a
// transcript that yields zero substantive (user/assistant/
// tool-use) entries used to render "" with NO signal to the operator —
// RenderResumedTranscript now warns via clidiag when it degrades to nothing,
// so every one of its three consumers (operations/engine_session.go,
// agentcoord/coord/spawner.go, cli/run.go) gets the signal automatically
// without each having to add its own rendered=="" check.
func TestRenderResumedTranscript_EmptyEntriesWarns(t *testing.T) {
	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	// Entries exist, but none are substantive (only a thinking entry, which
	// RenderResumedTranscript deliberately excludes) — MainThreadEntries'
	// real degrade case (an all-sidechain or otherwise empty transcript)
	// looks the same from RenderResumedTranscript's point of view: zero parts.
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeThinking, Content: "pondering"},
	}

	rendered := RenderResumedTranscript("test-harp", entries)
	assert.Empty(t, rendered, "no substantive entries must still render empty")
	assert.Contains(t, buf.String(), "test-harp",
		"an empty render must not be silent — every consumer relies on this warning instead of its own check")
}

// TestRenderResumedTranscript_LastEntryOverBudgetStillIncluded is a
// regression guard: the tail-budget loop initialized start to
// len(parts) and only advanced it AFTER the budget test passed, so a single
// trailing entry larger than resumeTranscriptBudget broke on its first
// iteration before start ever moved — parts[start:] was empty, yet the
// header ("its recorded history follows") and truncation note still
// printed. The fix must always include at least the last entry.
func TestRenderResumedTranscript_LastEntryOverBudgetStillIncluded(t *testing.T) {
	huge := strings.Repeat("x", resumeTranscriptBudget+1024)
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: huge},
	}

	rendered := RenderResumedTranscript("test-harp", entries)
	require.NotEmpty(t, rendered)
	assert.Contains(t, rendered, "its recorded history follows")
	// The header must not be a lie: SOME of the oversized entry's own text
	// must actually appear in the body, not just the announcement.
	assert.True(t, strings.Contains(rendered, "x"),
		"a single trailing entry over budget must still contribute content, not just an empty announcement")
}

// TestRenderResumedTranscript_TailBudgetKeepsMostRecent is a non-regression
// check that the ordinary (multi-entry, under-budget) tail-selection
// behavior is unchanged: earlier entries are dropped, the most recent ones
// (and only those) are kept, in original order.
func TestRenderResumedTranscript_TailBudgetKeepsMostRecent(t *testing.T) {
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeUser, Content: "first"},
		{Type: agent.EntryTypeAssistant, Content: "second"},
		{Type: agent.EntryTypeUser, Content: "third"},
	}

	rendered := RenderResumedTranscript("test-harp", entries)
	require.NotEmpty(t, rendered)
	assert.Contains(t, rendered, "user: first")
	assert.Contains(t, rendered, "assistant: second")
	assert.Contains(t, rendered, "user: third")
	assert.NotContains(t, rendered, "truncated", "everything fits comfortably under the 32KiB budget")
}
