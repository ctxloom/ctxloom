package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// convertLines (via vendorreader.ConvertJSONLLines) returns
// nil after producing zero canonical records for a file whose every
// response_item/event_msg line is one this build cannot map to an entry —
// the same shape as the already-fixed claude adapter, missing
// here.
func TestConvert_ResponseItemsWithNoEntriesIsAnError(t *testing.T) {
	testsupport.Isolate(t)
	// Both lines ARE response_item envelopes, but neither yields an entry:
	// an unrecognized response_item.type, and a message with no content.
	src := writeLines(t, "no-entries.jsonl",
		`{"type":"response_item","payload":{"type":"unmodeled_variant"}}`+"\n"+
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[]}}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := vendorreader.OpenAndReadJSONLLines("codex", src)
	require.NoError(t, lerr)
	err = convertLines(context.Background(), rec, lines)
	assert.Error(t, err, "zero entries from response_item lines this adapter claims to understand is a failed import, not a successful empty one")
}

// The same floor for a file that is entirely malformed.
func TestConvert_AllLinesMalformedIsAnError(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "all-malformed.jsonl", "{not json\n{also not json\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := vendorreader.OpenAndReadJSONLLines("codex", src)
	require.NoError(t, lerr)
	err = convertLines(context.Background(), rec, lines)
	assert.Error(t, err, "a file whose every line failed to parse must not import as a success")
}

// Discriminator: a file with only session_meta/turn_context lines (no
// response_item/event_msg content at all) is legitimately nothing to import.
func TestConvert_SessionMetaOnlyIsLegitimatelyEmpty(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "meta-only.jsonl",
		`{"type":"session_meta","payload":{"id":"s1"}}`+"\n"+
			`{"type":"turn_context","payload":{}}`+"\n")

	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	lines, lerr := vendorreader.OpenAndReadJSONLLines("codex", src)
	require.NoError(t, lerr)
	assert.NoError(t, convertLines(context.Background(), rec, lines),
		"no response_item/event_msg lines at all is 'nothing to import', not a failure")
}
