package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Shaped from the torn lines claude has written into real subagent files on
// this box (key names only were sampled): a record cut off mid-value, then
// the NEXT record appended on the same line with no newline between them.
// The glued record carries the top-level type, sessionId and uuid every
// claude conversation record does.
const (
	tornFragment = `{"parentUuid":"00000000-0000-4000-8000-00000000000a","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"cut off he`
	gluedLine    = `{"parentUuid":null,"isSidechain":true,"type":"user","sessionId":"s1","uuid":"00000000-0000-4000-8000-00000000000b","message":{"role":"user","content":"recovered"}}`
)

func TestDecodeLine(t *testing.T) {
	t.Run("a valid line is decoded as itself and is not torn", func(t *testing.T) {
		l, torn, err := decodeLine([]byte(gluedLine))
		require.NoError(t, err)
		assert.False(t, torn)
		assert.Equal(t, "user", l.Type)
		assert.Equal(t, "s1", l.SessionID)
	})
	t.Run("a cut-off line with nothing glued on stays malformed", func(t *testing.T) {
		_, torn, err := decodeLine([]byte(tornFragment))
		require.Error(t, err)
		assert.False(t, torn)
	})
	t.Run("a cut-off line ending in a complete NESTED object stays malformed", func(t *testing.T) {
		// The tail parses as an object with a type, but it is a content
		// block, not a record: it carries no sessionId or uuid.
		raw := `{"type":"user","sessionId":"s1","uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"x"}`
		_, torn, err := decodeLine([]byte(raw))
		require.Error(t, err)
		assert.False(t, torn)
	})
	t.Run("two complete records on one line are not a torn write", func(t *testing.T) {
		_, torn, err := decodeLine([]byte(gluedLine + gluedLine))
		require.Error(t, err)
		assert.False(t, torn)
	})
	t.Run("a torn line yields exactly the glued record", func(t *testing.T) {
		l, torn, err := decodeLine([]byte(tornFragment + gluedLine))
		require.NoError(t, err)
		assert.True(t, torn)
		var want line
		require.NoError(t, json.Unmarshal([]byte(gluedLine), &want))
		assert.Equal(t, want, l)
	})
}

// A torn line costs only its cut-off fragment: the glued record converts as
// any other line would, and the fragment is counted as vendor-truncated —
// not malformed — and reported.
func TestConvert_TornLineRecoversTheGluedRecord(t *testing.T) {
	testsupport.Isolate(t)
	var buf bytes.Buffer
	defer clidiag.SetSink(&buf)()

	src := writeLines(t, "torn.jsonl", tornFragment+gluedLine+"\n")
	lines, err := vendorreader.OpenAndReadJSONLLines("claude", src)
	require.NoError(t, err)
	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	acct, err := convertLines(context.Background(), rec, lines)
	require.NoError(t, err)
	require.NoError(t, rec.Close())

	assert.Equal(t, 1, acct.vendorTruncated)
	assert.Zero(t, acct.malformed)
	assert.Contains(t, buf.String(), vendorTruncatedLabel, "the lost fragment must still be reported")

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	var entries []string
	for _, r := range readRecords(t, path) {
		if r.Kind == transcript.KindEntry {
			entries = append(entries, r.Entry.Content)
		}
	}
	assert.Equal(t, []string{"recovered"}, entries)
}
