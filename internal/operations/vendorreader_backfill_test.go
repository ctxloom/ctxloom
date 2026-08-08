package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestBackfillVendorTranscripts_MixedSet covers `session backfill`'s core
// contract: a converted entry, a skipped one (nothing to do), and a failed
// one all get processed independently — the failure must not stop the
// entries after it (CLAUDE.md fault tolerance, mirrors
// distillMissingOrStale's per-entry warn-and-continue in session_cmd.go).
func TestBackfillVendorTranscripts_MixedSet(t *testing.T) {
	testsupport.Isolate(t)

	entries := []sessions.Entry{
		{ // converts cleanly
			HarpName:       "backfill-converts",
			Backend:        "codex",
			TranscriptPath: codexFixturePath,
		},
		{ // no adapter for this backend — skipped
			HarpName:       "backfill-skips-unregistered",
			Backend:        "opencode",
			TranscriptPath: codexFixturePath,
		},
		{ // no bound transcript — skipped
			HarpName: "backfill-skips-unbound",
			Backend:  "codex",
		},
		{ // bound path exists but isn't a readable transcript — fails
			HarpName:       "backfill-fails",
			Backend:        "codex",
			TranscriptPath: t.TempDir(),
		},
		{ // converts cleanly, a second engine
			HarpName:       "backfill-converts-claude",
			Backend:        "claude-code",
			TranscriptPath: claudeFixturePath,
		},
	}

	result := BackfillVendorTranscripts(context.Background(), entries)

	assert.ElementsMatch(t, []string{"backfill-converts", "backfill-converts-claude"}, result.Converted)
	assert.Equal(t, 2, result.Skipped)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed, "backfill-fails")

	// Every entry after the failing one must still have been processed —
	// prove it structurally rather than by ordering alone: the two
	// conversions and both skips are all accounted for above despite
	// "backfill-fails" sitting in the middle of the slice.
	assert.NotEmpty(t, canonicalLines(t, "backfill-converts"))
	assert.NotEmpty(t, canonicalLines(t, "backfill-converts-claude"))
}

func TestBackfillVendorTranscripts_Empty(t *testing.T) {
	testsupport.Isolate(t)
	result := BackfillVendorTranscripts(context.Background(), nil)
	assert.Empty(t, result.Converted)
	assert.Equal(t, 0, result.Skipped)
	assert.Empty(t, result.Failed)
}

// TestBackfillVendorTranscripts_Idempotent: re-running over a set that
// already fully converted on a prior pass converts nothing new — `session
// backfill` with no args must be safe to re-run at any time (its own doc
// comment's claim).
func TestBackfillVendorTranscripts_Idempotent(t *testing.T) {
	testsupport.Isolate(t)
	entries := []sessions.Entry{
		{HarpName: "backfill-idempotent", Backend: "codex", TranscriptPath: codexFixturePath},
	}

	first := BackfillVendorTranscripts(context.Background(), entries)
	require.Equal(t, []string{"backfill-idempotent"}, first.Converted)

	second := BackfillVendorTranscripts(context.Background(), entries)
	assert.Empty(t, second.Converted)
	assert.Equal(t, 1, second.Skipped)
	assert.Empty(t, second.Failed)
}

func TestBackfillResult_FailedHarps_Sorted(t *testing.T) {
	r := BackfillResult{Failed: map[string]string{
		"zebra-harp": "boom",
		"alpha-harp": "bang",
		"mid-harp":   "bust",
	}}
	assert.Equal(t, []string{"alpha-harp", "mid-harp", "zebra-harp"}, r.FailedHarps())
}

// TestBackfillVendorTranscripts_CancellationStopsRatherThanFailingEveryEntry:
// every registered adapter checks ctx per line
// (vendorreader.ConvertJSONLLines, kiro's own per-turn loop), so a cancelled
// backfill did not stop — it kept calling Convert, and each call returned
// context.Canceled, which the loop recorded as a per-harp FAILURE. The user
// got one "failed: <harp> (context canceled)" line per remaining session,
// libelling healthy sessions as broken imports for what was really one event:
// they interrupted the command. Worse, each of those attempts also ran
// ConvertVendorTranscript's partial-file cleanup.
//
// A cancelled context means STOP, so nothing after it is attempted and nothing
// is blamed.
func TestBackfillVendorTranscripts_CancellationStopsRatherThanFailingEveryEntry(t *testing.T) {
	testsupport.Isolate(t)

	entries := []sessions.Entry{
		{HarpName: "backfill-cancel-a", Backend: "codex", TranscriptPath: codexFixturePath},
		{HarpName: "backfill-cancel-b", Backend: "codex", TranscriptPath: codexFixturePath},
		{HarpName: "backfill-cancel-c", Backend: "claude-code", TranscriptPath: claudeFixturePath},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := BackfillVendorTranscripts(ctx, entries)

	assert.Empty(t, result.Failed,
		"interrupting the command is one event, not a per-session import failure — no harp may be reported as failed for it")
	assert.Empty(t, result.Converted, "nothing can have been converted after the context was already cancelled")
	assert.Equal(t, 0, result.Skipped,
		"an entry that was never attempted is not \"nothing to do\" either — a cancelled run stops rather than classifying the rest")
}
