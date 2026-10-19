package turnchange

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	claudereader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/claude"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// assistantTextLine is an assistant message carrying TEXT rather than a tool
// call — what a turn ends with.
func assistantTextLine(uuid, msgID, text string, sidechain bool) string {
	return `{"type":"assistant","isSidechain":` + boolStr(sidechain) + `,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"` + msgID +
		`","type":"message","role":"assistant","content":[{"type":"text","text":"` + text +
		`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:02.000Z"}`
}

// TestReadTranscript_ReadsTheVendorFormatEndToEnd proves the exported reader
// converts the REAL vendor format into events, not just hand-built ones.
//
// MUTATION — have ReadTranscript return c.events[:0] — turns this red.
func TestReadTranscript_ReadsTheVendorFormatEndToEnd(t *testing.T) {
	p := writeTranscript(t,
		promptLine("capture my next step", "u1"),
		assistantToolLine("a1", "msg_1", "Read", `{"file_path":"/plan.md"}`, false),
		assistantTextLine("a2", "msg_2", "Next I will merge the branch.", false),
	)

	evs, err := ReadTranscript(context.Background(), afero.NewOsFs(), claudereader.Adapter{}, p)
	require.NoError(t, err)
	require.NotEmpty(t, evs, "the vendor transcript must yield events")

	assert.Contains(t, contents(evs), "Next I will merge the branch.")
}

// TestReadTranscript_UnreadableFileErrors pins that a read failure is
// REPORTED rather than reported as an empty transcript — the caller must be
// able to say why it has nothing to classify.
//
// MUTATION — swallow the Convert error and return (c.events, nil) — red.
func TestReadTranscript_UnreadableFileErrors(t *testing.T) {
	_, err := ReadTranscript(context.Background(), afero.NewOsFs(), claudereader.Adapter{}, "/nonexistent/transcript.jsonl")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "no error"))
}

// TestReadTranscript_ReadsThroughTheGivenFs: the transcript exists only in
// the injected fs, so a read that reached for the disk would fail to open it.
func TestReadTranscript_ReadsThroughTheGivenFs(t *testing.T) {
	raw, err := os.ReadFile(writeTranscript(t,
		promptLine("capture my next step", "u1"),
		assistantTextLine("a1", "msg_1", "Next I will merge the branch.", false),
	))
	require.NoError(t, err)
	const src = "/turnchange-memfs-only/transcript.jsonl"
	_, statErr := os.Stat(src)
	require.ErrorIs(t, statErr, fs.ErrNotExist, "the transcript must be absent from disk")
	mem := afero.NewMemMapFs()
	testsupport.WriteFile(t, mem, src, raw, 0o644)

	evs, err := ReadTranscript(context.Background(), mem, claudereader.Adapter{}, src)
	require.NoError(t, err)
	assert.Contains(t, contents(evs), "Next I will merge the branch.")
}

// contents is every entry's content in evs, in order.
func contents(evs []agent.ChatEvent) []string {
	var out []string
	for _, e := range evs {
		if e.Entry != nil {
			out = append(out, e.Entry.Content)
		}
	}
	return out
}
