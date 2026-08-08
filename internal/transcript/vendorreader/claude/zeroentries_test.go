package claude

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

func writeLines(t *testing.T, name, content string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))
	return src
}

// The reader could parse a whole file, produce ZERO canonical
// entries, and return a nil error — every layer above then reports a
// successful import of nothing. A file whose conversational lines all failed
// to yield entries is a parse failure, and must say so.
func TestConvert_ConversationalLinesWithNoEntriesIsAnError(t *testing.T) {
	testsupport.Isolate(t)
	// Both lines ARE user/assistant lines — the shapes this adapter exists to
	// convert — but neither yields a single entry (unrecognized content shape,
	// empty block array).
	src := writeLines(t, "no-entries.jsonl",
		`{"type":"user","sessionId":"s1","message":{"role":"user","content":{"unexpected":"shape"}}}`+"\n"+
			`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[]}}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, src)
	assert.Error(t, err, "zero entries from lines this adapter claims to understand is a failed import, not a successful empty one")
}

// The same floor for a file that is entirely malformed: every line skipped,
// nothing converted, and today a nil error.
func TestConvert_AllLinesMalformedIsAnError(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "all-malformed.jsonl", "{not json\n{also not json\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, src)
	assert.Error(t, err, "a file whose every line failed to parse must not import as a success")
}

// Discriminator: a file with no conversational lines at all is legitimately
// nothing to import (administrative UI/session bookkeeping only) and stays a
// success.
func TestConvert_AdminOnlyFileIsLegitimatelyEmpty(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "admin-only.jsonl",
		`{"type":"progress","sessionId":"s1"}`+"\n"+`{"type":"ai-title","sessionId":"s1"}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	assert.NoError(t, Adapter{}.Convert(context.Background(), rec, src),
		"no user/assistant lines at all is 'nothing to import', not a failure")
}

// Vendor content dropped on the floor — an unmodeled content block
// and a non-text tool_result element — used to vanish with no counter, no
// diagnostic, and no way for an operator to learn anything was dropped.
func TestConvert_DroppedVendorContentIsReported(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "drops.jsonl",
		`{"type":"user","sessionId":"s1","message":{"role":"user","content":[`+
			`{"type":"text","text":"look at this"},`+
			`{"type":"image","source":{"data":"AAAA"}}]}}`+"\n"+
			`{"type":"user","sessionId":"s1","message":{"role":"user","content":[`+
			`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","data":"BBBB"}]}]}}`+"\n")

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, src))
	require.NoError(t, rec.Close())

	assert.Contains(t, buf.String(), "image",
		"dropped vendor content must be reported, naming what was dropped")
}

// A vendor `usage` shape ctxloom does not control (Anthropic
// renames/drops a key) degrades every counter to its Go zero value via plain
// json.Unmarshal, with no error — and previously no test pinned that this is
// what actually happens. This is NOT a bug to fix: agent.TurnMeta's own
// contract (chat.go) is "a zero field means unknown", so a drifted/absent
// usage key legitimately reads as "unknown", not a lie — the same honest
// degrade this adapter already relies on elsewhere. The gap the census row
// names is coverage, not behavior: this test turns the previously-implicit
// assumption into a checked contract, and confirms a partial drift (this
// message's usage, only) does not cost the conversational content itself —
// checkFloor's file-wide floor could never catch a partial per-message drift
// like this one.
func TestConvert_DriftedUsageShapeDegradesToZeroNotError(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "drifted-usage.jsonl",
		`{"type":"user","sessionId":"s1","message":{"role":"user","content":"hello"}}`+"\n"+
			`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],`+
			`"usage":{"inputTokens":10,"outputTokens":5}}}`+"\n") // camelCase keys: not this struct's json tags

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)

	require.NoError(t, Adapter{}.Convert(context.Background(), rec, src),
		"a drifted usage shape must not fail the whole import — the conversational content is still good")
	require.NoError(t, rec.Close())

	path, perr := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, perr)
	recs := readRecords(t, path)

	var sawComplete, sawAssistantEntry bool
	for _, r := range recs {
		if r.Kind == transcript.KindComplete {
			sawComplete = true
			require.NotNil(t, r.Complete)
			assert.Zero(t, r.Complete.InputTokens, "an unrecognized usage key shape must degrade to the documented zero/unknown sentinel, not a guessed value")
			assert.Zero(t, r.Complete.OutputTokens)
		}
		if r.Kind == transcript.KindEntry && r.Entry != nil && r.Entry.Type == "assistant" {
			sawAssistantEntry = true
			assert.Equal(t, "hi", r.Entry.Content, "the conversational content itself must survive a drift confined to the sibling usage block")
		}
	}
	assert.True(t, sawComplete, "the turn boundary must still be recorded even with a drifted usage block")
	assert.True(t, sawAssistantEntry)
}
