package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/importer"
)

func writeLines(t *testing.T, name, content string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))
	return src
}

// U146-F01: convertLines returns nil after discarding every line of a file
// (e.g. every step's Status has drifted away from "DONE"), producing zero
// entries with no floor check — combined with the lazy Recorder (a file is
// only created on the first SUCCESSFUL Record) this is a permanent,
// self-repeating false success: nothing ever gets written, nothing ever
// errors, and no caller ever learns the vendor format drifted.
func TestConvert_AllStepsWrongStatusIsAnError(t *testing.T) {
	testsupport.Isolate(t)
	// Both steps ARE the two types this adapter converts, but neither is
	// "DONE" (see U146-F02: the status gate applies before the type switch),
	// so both are skipped and zero entries come out.
	src := writeLines(t, "wrong-status.jsonl",
		`{"type":"USER_INPUT","status":"RUNNING","content":"<USER_REQUEST>hi</USER_REQUEST>"}`+"\n"+
			`{"type":"PLANNER_RESPONSE","status":"RUNNING","content":"ok"}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := importer.OpenAndReadJSONLLines("antigravity", src)
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

	lines, lerr := importer.OpenAndReadJSONLLines("antigravity", src)
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

	lines, lerr := importer.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)
	assert.NoError(t, convertLines(context.Background(), rec, lines),
		"no USER_INPUT/PLANNER_RESPONSE lines at all is 'nothing to import', not a failure")
}
