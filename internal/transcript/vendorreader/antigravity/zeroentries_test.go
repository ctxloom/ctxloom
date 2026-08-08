package antigravity

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
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

func writeLines(t *testing.T, name, content string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))
	return src
}

// convertLines once returned nil after discarding every line of a file
// (e.g. every step's Status has drifted away from "DONE"), producing zero
// entries with no floor check — combined with the lazy Recorder (a file is
// only created on the first SUCCESSFUL Record) this is a permanent,
// self-repeating false success: nothing ever gets written, nothing ever
// errors, and no caller ever learns the vendor format drifted.
func TestConvert_AllStepsWrongStatusIsAnError(t *testing.T) {
	testsupport.Isolate(t)
	// Both steps ARE the two types this adapter converts, but neither is
	// "DONE" (the status gate applies before the type switch),
	// so both are skipped and zero entries come out.
	src := writeLines(t, "wrong-status.jsonl",
		`{"type":"USER_INPUT","status":"RUNNING","content":"<USER_REQUEST>hi</USER_REQUEST>"}`+"\n"+
			`{"type":"PLANNER_RESPONSE","status":"RUNNING","content":"ok"}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)
	err = convertLines(context.Background(), rec, lines)
	assert.Error(t, err, "zero entries from steps this adapter claims to understand is a failed import, not a successful empty one")
}

// The same floor for a file that is entirely malformed.
func TestConvert_AllLinesMalformedIsAnError(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "all-malformed.jsonl", "{not json\n{also not json\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)
	err = convertLines(context.Background(), rec, lines)
	assert.Error(t, err, "a file whose every line failed to parse must not import as a success")
}

// Discriminator: a file with no USER_INPUT/PLANNER_RESPONSE lines at all
// (only step types this adapter never converts) is legitimately nothing to
// import and stays a success.
func TestConvert_UnconvertedStepTypesOnlyIsLegitimatelyEmpty(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "admin-only.jsonl",
		`{"type":"CHECKPOINT","status":"DONE","content":"x"}`+"\n"+
			`{"type":"GENERIC","status":"DONE","content":"y"}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)
	assert.NoError(t, convertLines(context.Background(), rec, lines),
		"no USER_INPUT/PLANNER_RESPONSE lines at all is 'nothing to import', not a failure")
}

// TestConvert_ErrorMessageStepIsRecorded is the other half of the ERROR_MESSAGE pin,
// at the convertLines seam rather than the pure mapper: it proves the
// type/status gating actually LETS an ERROR_MESSAGE step reach stepEvent.
// The status is deliberately NOT "DONE" — no ERROR_MESSAGE step exists in any
// capture on this box, so this adapter has never observed what status one
// carries, and gating a failure notice on a status vocabulary nobody has
// verified would silently re-open exactly the drop this row is about.
func TestConvert_ErrorMessageStepIsRecorded(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "error-step.jsonl",
		`{"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>go</USER_REQUEST>"}`+"\n"+
			`{"type":"ERROR_MESSAGE","status":"ERROR","content":"model request failed: 503"}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)

	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)
	require.NoError(t, convertLines(context.Background(), rec, lines))
	require.NoError(t, rec.Close())

	path, perr := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, perr)
	recs := readRecords(t, path)

	var sawError bool
	for _, r := range recs {
		if r.Entry != nil && r.Entry.Type == "system" {
			assert.Equal(t, "model request failed: 503", r.Entry.Content)
			sawError = true
		}
	}
	assert.True(t, sawError, "an ERROR_MESSAGE step must leave a trace in the canonical transcript, not vanish")
}

// TestConvert_DriftedContentIsNotReportedAsUnparseableJSON pins a drifted-content
// diagnostic. Every line below is perfectly well-formed JSON; what changed is the SHAPE of
// `content` — a structured object where this build expects a bare string. With
// `Content string`, json.Unmarshal fails on the whole line and it is discarded
// through the identical `continue` a truncated, genuinely corrupt line takes,
// so the import reports "all N line(s) failed to parse as JSON" — a diagnostic
// that is simply false and points whoever reads it at the wrong problem
// (a broken file rather than a vendor format this build no longer parses).
// The two failures are different and must be reported differently.
func TestConvert_DriftedContentIsNotReportedAsUnparseableJSON(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "drifted-content.jsonl",
		`{"type":"USER_INPUT","status":"DONE","content":{"text":"hi"}}`+"\n"+
			`{"type":"PLANNER_RESPONSE","status":"DONE","content":{"parts":[{"text":"ok"}]}}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)
	err = convertLines(context.Background(), rec, lines)
	require.Error(t, err, "zero entries out of two steps this adapter maps is a failed import")
	assert.NotContains(t, err.Error(), "failed to parse as JSON",
		"these lines ARE valid JSON; only the shape of `content` drifted")
	assert.Contains(t, err.Error(), "content",
		"the diagnostic must name the field whose shape this build can no longer read")
}

// TestConvert_DriftedContentKeepsReadableStepsAndWarns is the degrade-to-partial
// half of the same pin: one step's `content` has drifted, the other's has not. The
// readable step must still import (vendorreader.VendorAdapter's contract), and the
// dropped one must be reported rather than vanishing — the same "a drop nobody
// can observe is indistinguishable from content that was never there"
// discipline claude's reader already applies to unmodeled block types.
func TestConvert_DriftedContentKeepsReadableStepsAndWarns(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "mixed-drift.jsonl",
		`{"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>still here</USER_REQUEST>"}`+"\n"+
			`{"type":"PLANNER_RESPONSE","status":"DONE","content":{"parts":[{"text":"lost"}]}}`+"\n")

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)

	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)
	require.NoError(t, convertLines(context.Background(), rec, lines),
		"a readable step alongside a drifted one is a partial import, not a failure")
	require.NoError(t, rec.Close())

	path, perr := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, perr)
	recs := readRecords(t, path)
	require.Len(t, recs, 1)
	assert.Equal(t, "still here", recs[0].Entry.Content)

	assert.Contains(t, warnings.String(), "content",
		"the step whose content shape drifted must be reported, not silently dropped")
}
