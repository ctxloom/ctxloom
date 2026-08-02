package tui

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var exportItems = []feedItem{
	{role: "user", ts: time.Unix(1700000000, 0), text: "refactor the parser"},
	{role: "tool_use", toolName: "Edit", toolInput: `{"file":"tokenizer.go"}`},
	{role: "tool_result", toolName: "Edit", toolOutput: "ok (2 hunks)"},
	{role: "notice", text: "… 3 live events dropped"},
	{role: "assistant", text: "done; deferred: lexer tests", sidechain: true},
}

func TestTranscriptNDJSON_ByteExact(t *testing.T) {
	data, err := transcriptNDJSON(exportItems)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	require.Len(t, lines, 4, "notices are viewer chrome, not transcript content")

	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "user", first["type"])
	assert.Equal(t, "refactor the parser", first["content"])
	assert.Equal(t, float64(1700000000), first["timestamp_unix"])

	var tool map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &tool))
	assert.Equal(t, "Edit", tool["tool_name"])
	assert.Equal(t, map[string]any{"file": "tokenizer.go"}, tool["tool_input"],
		"tool input stays raw JSON, not a quoted string")

	var last map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[3]), &last))
	assert.Equal(t, true, last["sidechain"])
}

func TestTranscriptText_RendersExpanded(t *testing.T) {
	text := string(transcriptText(exportItems, 100))
	assert.Contains(t, text, "user  > refactor the parser")
	assert.Contains(t, text, "Edit")
	assert.Contains(t, text, "ok (2 hunks)") // tool output expanded in exports
	assert.NotContains(t, text, "live events dropped", "notices excluded")
}

// s (save) and y (copy) are two spellings of "give me this transcript", and
// they render it through renderItem at a width each supplies for itself. The
// widths must be one value: a divergence silently gives the file and the
// clipboard different line breaks for the same feed.
func TestExportedTextAndCopiedTextAgreeOnRenderWidth(t *testing.T) {
	items := []feedItem{{role: "user", text: strings.Repeat("x", 250)}}
	dir := t.TempDir()

	path, err := exportTranscript(dir, "h1", "txt", items, time.Date(2026, 7, 7, 10, 15, 0, 0, time.UTC))
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	assert.Equal(t, strings.TrimRight(string(data), "\n"), copyText(items, 0, false),
		"the saved file and the clipboard hold the same rendering")
}

func TestExportTranscript_WritesUnderSessionDir(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 7, 10, 15, 0, 0, time.UTC)

	path, err := exportTranscript(dir, "swift-elm-fox", "ndjson", exportItems, now)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "transcript-swift-elm-fox-20260707T101500.ndjson"), path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	want, err := transcriptNDJSON(exportItems)
	require.NoError(t, err)
	assert.Equal(t, want, data, "the file carries exactly the rendered transcript bytes")

	txtPath, err := exportTranscript(dir, "swift-elm-fox", "txt", exportItems, now)
	require.NoError(t, err)
	txt, err := os.ReadFile(txtPath)
	require.NoError(t, err)
	assert.Equal(t, transcriptText(exportItems, 100), txt)
}

// A feed that holds only viewer chrome renders to zero bytes. Writing that
// file and reporting "saved <path>" is the project's characteristic silent
// no-op: exit 0, success message, zero bytes delivered.
func TestExportTranscript_EmptyRenderIsAnError(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 7, 10, 15, 0, 0, time.UTC)
	notices := []feedItem{
		{role: "notice", text: "… 3 live events dropped"},
		{role: "notice", text: "… 1 live event dropped"},
	}

	for _, kind := range []string{"txt", "ndjson"} {
		path, err := exportTranscript(dir, "swift-elm-fox", kind, notices, now)
		require.ErrorIs(t, err, errEmptyTranscript, "kind=%s", kind)
		assert.Empty(t, path, "kind=%s: no path may be reported for a refused export", kind)
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a refused export must not leave a 0-byte file behind")
}

// An export whose feed is genuinely empty is the same refusal, not a
// zero-byte file.
func TestExportTranscript_NoItemsIsAnError(t *testing.T) {
	dir := t.TempDir()
	_, err := exportTranscript(dir, "swift-elm-fox", "txt", nil, time.Now())
	require.ErrorIs(t, err, errEmptyTranscript)
}

func TestOSC52Copy_Encoding(t *testing.T) {
	got := osc52Copy("hello ✂ clipboard")
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello ✂ clipboard")) + "\x07"
	assert.Equal(t, want, string(got))
}

func TestCopyText_SelectedVsVisible(t *testing.T) {
	items := []feedItem{
		{role: "user", text: "one"},
		{role: "assistant", text: "two"},
	}
	sel := copyText(items, 1, true)
	assert.Contains(t, sel, "two")
	assert.NotContains(t, sel, "one", "feed focus copies the cursor entry only")

	all := copyText(items, 1, false)
	assert.Contains(t, all, "one")
	assert.Contains(t, all, "two")
}

// TestExportTranscript_RejectsAnUnknownKind is the regression guard: `kind`
// was used for TWO things -- selecting the renderer and
// supplying the filename extension -- with a `default:` arm that fell through
// to the TEXT renderer. Any kind other than "ndjson" therefore produced
// role-tagged plain text in a file whose extension claimed otherwise:
// exportTranscript(..., "json", ...) wrote transcript-<harp>-<ts>.json full of
// text, and reported "saved <path>" over it. The switch is now exhaustive and
// an unrecognised kind is a loud error rather than a file that lies about its
// own contents.
func TestExportTranscript_RejectsAnUnknownKind(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	for _, kind := range []string{"json", "md", "", "NDJSON", "text"} {
		t.Run(kind, func(t *testing.T) {
			path, err := exportTranscript(dir, "swift-elm-fox", kind, exportItems, now)
			require.Error(t, err, "an unrecognised kind must not silently render text under that extension")
			assert.Empty(t, path)

			entries, rerr := os.ReadDir(dir)
			require.NoError(t, rerr)
			assert.Empty(t, entries, "a refused export must leave no file behind")
		})
	}
}
