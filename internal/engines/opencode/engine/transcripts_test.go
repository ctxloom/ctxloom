package opencodeengine

import (
	"context"
	_ "embed"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/harpmarker"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// recorded is a bridge transcript RECORDED from opencode 1.18.4 driven by
// `opencode run` against a scripted OpenAI-compatible stub (no model): the
// generated bridge plugin wrote it at idle. The turn ran one bash call; a
// turn_start hook added mail as a synthetic part, a session_start hook the
// harp marker. Only the throwaway run's paths are rewritten to /work.
//
//go:embed testdata/ses_ee5be9a8effep51RbhRcX5cfeY.jsonl
var recorded []byte

const recordedSession = "ses_ee5be9a8effep51RbhRcX5cfeY"

// eventSink is an in-memory transcript.Recorder.
type eventSink struct{ events []agent.ChatEvent }

func (s *eventSink) Record(ev agent.ChatEvent) error { s.events = append(s.events, ev); return nil }
func (*eventSink) Close() error                      { return nil }

func convert(t *testing.T, body []byte) ([]agent.ChatEvent, error) {
	t.Helper()
	mem := afero.NewMemMapFs()
	src := "/n/opencode/ctxloom/" + recordedSession + ".jsonl"
	testsupport.WriteFile(t, mem, src, body, 0o600)
	sink := &eventSink{}
	err := Adapter{}.Convert(context.Background(), mem, sink, src)
	return sink.events, err
}

// THE RECORDED TRANSCRIPT converts to the conversation it holds: the session
// first (from the bridge's header), the user's prompt and the mail the hook
// added, the bash call and its result, the answer — in the file's order.
func TestConvert_TheRecordedBridgeTranscript(t *testing.T) {
	evs, err := convert(t, recorded)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	require.NotNil(t, evs[0].Session)
	assert.Equal(t, recordedSession, evs[0].Session.SessionID)

	type row struct {
		Type    agent.SessionEntryType
		Content string
		Tool    string
	}
	var got []row
	for _, ev := range evs[1:] {
		require.NotNil(t, ev.Entry, "every later event is an entry")
		e := ev.Entry
		got = append(got, row{e.Type, e.Content + e.ToolOutput, e.ToolName})
		assert.False(t, e.Timestamp.IsZero(), "%s carries its time", e.Type)
	}
	assert.Equal(t, []row{
		{agent.EntryTypeUser, `"please use-tool now"`, ""},
		{agent.EntryTypeUser, "MAIL: a letter for you", ""},
		{agent.EntryTypeToolUse, "", "bash"},
		{agent.EntryTypeToolResult, "stub-tool-ran\n", "bash"},
		{agent.EntryTypeAssistant, "stub answer (MAIL)", ""},
	}, got)
	assert.JSONEq(t, `{"command":"echo stub-tool-ran","description":"echo"}`, string(evs[3].Entry.ToolInput))
	assert.Equal(t, "call_1", evs[3].Entry.ToolCallID)
}

// The harp marker the session_start hook answered with is in the raw file, so
// the by-content attribution finds the owner without converting anything.
func TestRecordedTranscript_CarriesTheHarpMarker(t *testing.T) {
	first := strings.SplitN(string(recorded), "\n", 2)[0]
	assert.Equal(t, "smoke-harp", harpmarker.Scan([]byte(first)))
}

// A malformed line is skipped; a file of nothing but unreadable lines is
// refused, never converted to an empty conversation; a header alone is a
// session with nothing said yet.
func TestConvert_DegradesLineByLine(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	evs, err := convert(t, []byte(lines[0]+"\nnot json\n"+lines[1]+"\n"))
	require.NoError(t, err)
	assert.Len(t, evs, 3, "the session and the two user entries; the bad line skipped")

	_, err = convert(t, []byte("not json\n{\"x\":1}\n"))
	require.ErrorContains(t, err, "not a bridge transcript")

	evs, err = convert(t, []byte(lines[0]+"\n"))
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.NotNil(t, evs[0].Session)
}

// The span the reader is declared for holds the pinned version it cites.
func TestVersionedAdapters_CiteThePin(t *testing.T) {
	require.Len(t, VersionedAdapters, 1)
	a := VersionedAdapters[0]
	ok, err := a.Range.Contains(a.ValidatedVersion)
	require.NoError(t, err)
	assert.True(t, ok)
	_, err = vendorreader.SelectAdapter("opencode", "1.18.4", "", VersionedAdapters)
	require.NoError(t, err, "the version the fixture was recorded on is read")
	assert.Len(t, Transcripts(), 1)
}
