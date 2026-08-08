package operations

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// vendorFileWithLines writes the first n lines of the claude fixture to a
// writable temp file and returns its path, so a test can grow a vendor
// transcript the way a live session does. n <= 0 writes the whole fixture.
func vendorFileWithLines(t *testing.T, n int) string {
	t.Helper()
	raw, err := os.ReadFile(claudeFixturePath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Greater(t, len(lines), 2, "fixture must have enough lines to truncate meaningfully")
	if n > 0 && n < len(lines) {
		lines = lines[:n]
	}
	p := filepath.Join(t.TempDir(), "vendor-transcript.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return p
}

// tsPattern matches a canonical record's "ts" field — the moment the RECORDER
// wrote the line, not anything about the conversation.
var tsPattern = regexp.MustCompile(`"ts":"[^"]*"`)

// withoutRecordTimes strips the recording timestamp from each line so two
// conversions of the same vendor transcript can be compared for the content
// they carry. A re-conversion legitimately restamps: no VendorAdapter preserves
// an original record time distinct from the vendor event, so the stamp is when
// the line was written, and comparing it would fail on a working refresh.
func withoutRecordTimes(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, tsPattern.ReplaceAllString(l, `"ts":"<stamped>"`))
	}
	return out
}

func claudeEntry(harp, vendorPath string) sessions.Entry {
	return sessions.Entry{
		HarpName:       harp,
		Backend:        config.BackendClaudeCode,
		TranscriptPath: vendorPath,
		EngineVersion:  "2.1.225",
	}
}

// TestRefreshVendorTranscript_ReplacesRatherThanAppends is the trap this whole
// verb exists to avoid, and the reason it could not simply be
// ConvertVendorTranscript with its guard removed.
//
// A Recorder APPENDS, and no VendorAdapter can resume from an offset — it
// re-reads its source from the beginning every time. So a re-conversion that
// wrote to the canonical file directly would leave TWO full copies of the
// transcript concatenated, which reads back as a session where every turn
// happened twice. Nothing downstream would report an error; the distillation
// would just quietly describe a doubled conversation.
func TestRefreshVendorTranscript_ReplacesRatherThanAppends(t *testing.T) {
	testsupport.Isolate(t)
	harp := "refresh-replaces-harp"
	e := claudeEntry(harp, claudeFixturePath)

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)
	first := canonicalLines(t, harp)
	require.NotEmpty(t, first)

	converted, err = RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted, "a refresh re-converts rather than reporting nothing to do")

	second := canonicalLines(t, harp)
	assert.Len(t, second, len(first), "refresh must REPLACE the canonical transcript, not append a second copy of it")
	assert.Equal(t, withoutRecordTimes(first), withoutRecordTimes(second), "re-converting an unchanged vendor transcript must reproduce the same conversation")
}

// TestRefreshVendorTranscript_PicksUpVendorGrowth is the property the live
// /recover path depends on: a session converted once and then written to some
// more must read back with the new turns in it.
//
// Without the refresh, the second recovery in a session returns a transcript
// frozen at the first — complete-looking and confidently missing everything
// that happened since, which is strictly worse than the "nothing captured"
// failure it replaced.
func TestRefreshVendorTranscript_PicksUpVendorGrowth(t *testing.T) {
	testsupport.Isolate(t)
	harp := "refresh-growth-harp"
	vendorPath := vendorFileWithLines(t, 2)
	e := claudeEntry(harp, vendorPath)

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)
	before := canonicalLines(t, harp)
	require.NotEmpty(t, before)

	// The session keeps going: the engine appends to its own store.
	grown, err := os.ReadFile(vendorFileWithLines(t, 0))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(vendorPath, grown, 0o644))

	converted, err = RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)

	after := canonicalLines(t, harp)
	assert.Greater(t, len(after), len(before), "the refreshed transcript must contain the turns added since the first conversion")
	// A count alone would be satisfied by a refresh that appended garbage, so
	// name a turn that exists ONLY in the grown half of the vendor file.
	assert.Contains(t, strings.Join(after, "\n"), "[Request interrupted by user for tool use]",
		"the refreshed transcript must carry the turns the session added, not merely more lines")
	// The prefix is deliberately NOT compared: a session record legitimately
	// gets richer as more of the vendor file is read (the model and permission
	// mode arrive on a later line), so an unchanged-prefix assertion would fail
	// on a working refresh.
}

// TestRefreshVendorTranscript_KeepsExistingTranscriptWhenNothingToConvert pins
// the degradation that makes it safe to call this before every live read: a
// harp whose vendor store cannot be located must keep the canonical transcript
// it already has. Refreshing is an attempt to improve the record, and an
// attempt that fails must never leave the caller with less than it started
// with.
func TestRefreshVendorTranscript_KeepsExistingTranscriptWhenNothingToConvert(t *testing.T) {
	testsupport.Isolate(t)
	harp := "refresh-nothing-harp"

	converted, err := ConvertVendorTranscript(context.Background(), claudeEntry(harp, claudeFixturePath))
	require.NoError(t, err)
	require.True(t, converted)
	before := canonicalLines(t, harp)
	require.NotEmpty(t, before)

	// Same harp, but the vendor transcript is gone from where the index says.
	gone := filepath.Join(t.TempDir(), "vanished.jsonl")
	converted, err = RefreshVendorTranscript(context.Background(), claudeEntry(harp, gone))
	require.NoError(t, err)
	assert.False(t, converted, "nothing locatable to convert is 'nothing to do', not a failure")

	assert.Equal(t, before, canonicalLines(t, harp), "a refresh that converts nothing must leave the existing transcript byte-identical — nothing was rewritten, so even the stamps must match")
}

// TestRefreshVendorTranscript_FailedRefreshKeepsTheTranscriptItHad is the claim
// the temp-then-rename write actually protects, and the one with teeth: a
// refresh REPLACES a transcript that already exists, so a conversion that dies
// partway through has a complete record in its hands and must not end up
// destroying it.
//
// Written after a surviving mutation. Pointing the conversion straight at the
// canonical file — no temporary sibling — passed the whole suite, because a
// refresh that removes-then-rewrites is indistinguishable from one that
// renames INTO PLACE for as long as every conversion succeeds. The difference
// is only visible on failure, which is where the data loss would be.
func TestRefreshVendorTranscript_FailedRefreshKeepsTheTranscriptItHad(t *testing.T) {
	testsupport.Isolate(t)
	harp := "refresh-failure-harp"
	e := sessions.Entry{HarpName: harp, Backend: "codex", TranscriptPath: codexFixturePath, EngineVersion: stubEngineVersion}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)
	before := canonicalLines(t, harp)
	require.NotEmpty(t, before, "the harp must start with a real transcript, or there is nothing to lose")

	// The engine's store is now unreadable partway through — a bad byte on a
	// line the first conversion never reached.
	orig := vendorReaderRegistry["codex"]
	vendorReaderRegistry["codex"] = vendorReaderEntry{
		adapters: stubVersionedAdapter(partialFailAdapter{n: 3}),
		locate:   orig.locate,
	}
	defer func() { vendorReaderRegistry["codex"] = orig }()

	_, err = RefreshVendorTranscript(context.Background(), e)
	require.Error(t, err, "a conversion that dies partway must surface, not be swallowed")

	after := canonicalLines(t, harp)
	assert.Equal(t, before, after,
		"a failed refresh must leave the transcript it was replacing exactly as it was — losing a complete record to a half-finished re-read is worse than not refreshing at all")
	assert.NotContains(t, strings.Join(after, "\n"), "partial content",
		"none of the failed conversion's output may reach the canonical transcript")
}

// TestRefreshVendorTranscript_LeavesNoRebuildArtifact pins that the temporary
// file conversion writes through is not left in the harp's persist directory.
// It sits beside the canonical transcript, so a stray one is both litter and a
// second thing a future reader could mistake for a transcript.
func TestRefreshVendorTranscript_LeavesNoRebuildArtifact(t *testing.T) {
	testsupport.Isolate(t)
	harp := "refresh-artifact-harp"

	_, err := RefreshVendorTranscript(context.Background(), claudeEntry(harp, claudeFixturePath))
	require.NoError(t, err)

	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	_, err = os.Stat(canonPath + ".rebuild")
	assert.True(t, os.IsNotExist(err), "the .rebuild temp must be renamed into place, never left behind")
}
