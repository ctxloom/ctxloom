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
