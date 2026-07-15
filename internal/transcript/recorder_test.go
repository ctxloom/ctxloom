package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// readRecordedLines reads back a harp's canonical transcript file and decodes
// every line into a Record, in file order.
func readRecordedLines(t *testing.T, harp string) []Record {
	t.Helper()
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var recs []Record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r Record
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		recs = append(recs, r)
	}
	require.NoError(t, scanner.Err())
	return recs
}

// TestRecorder_RoundTrip_RealKiroToolCallTurn feeds the REAL kiro tool-calling
// turn (see testdata/fixtures/MANIFEST.json — captured from a live kiro CLI
// session on this box, ~/.kiro/sessions/cli/3808ae21-...jsonl) through the
// Recorder as agent.ChatEvents (the shape mapSessionUpdate would actually
// produce), then reads the written file back off disk and asserts the real
// payload survived byte-for-byte — not merely that five lines exist.
func TestRecorder_RoundTrip_RealKiroToolCallTurn(t *testing.T) {
	testsupport.Isolate(t)
	harp := "kiro-roundtrip-harp"

	rec, err := NewRecorder(harp, "kiro")
	require.NoError(t, err)

	events := []agent.ChatEvent{
		{Session: &agent.ChatSessionInfo{
			SessionID: "3808ae21-1a82-4c80-83f1-132256adce36",
			Model:     "kiro-default",
		}},
		{Entry: &agent.SessionEntry{
			Type:    agent.EntryTypeUser,
			Content: "Read the file probe6.txt in the current directory using your file read tool, then tell me the contents.",
		}},
		{Entry: &agent.SessionEntry{
			Type:      agent.EntryTypeToolUse,
			ToolName:  "read",
			ToolInput: json.RawMessage(`{"__tool_use_purpose":"Reading probe6.txt from current directory","operations":[{"mode":"Line","path":"probe6.txt"}]}`),
		}},
		{Entry: &agent.SessionEntry{
			Type:       agent.EntryTypeToolResult,
			ToolName:   "read",
			ToolOutput: "sixth probe file contents for tool-call capture",
			IsError:    false,
		}},
		{Entry: &agent.SessionEntry{
			Type:    agent.EntryTypeAssistant,
			Content: "The contents of `probe6.txt` are:\n\n```\nsixth probe file contents for tool-call capture\n```",
		}},
	}
	for _, ev := range events {
		require.NoError(t, rec.Record(ev))
	}
	require.NoError(t, rec.Close())

	recs := readRecordedLines(t, harp)
	require.Len(t, recs, 5)

	// seq is gap-free from 0, and every line carries the schema version + the
	// harp/engine/session_id stamped by the recorder (session_id latches onto
	// every line from the moment the session line is observed, including that
	// line itself).
	for i, r := range recs {
		assert.Equal(t, i, r.Seq)
		assert.Equal(t, SchemaVersion, r.V)
		assert.Equal(t, harp, r.Harp)
		assert.Equal(t, "kiro", r.Engine)
		assert.Equal(t, "3808ae21-1a82-4c80-83f1-132256adce36", r.SessionID)
		assert.False(t, r.TS.IsZero(), "ts must be stamped")
	}

	require.NotNil(t, recs[2].Entry)
	assert.Equal(t, "tool_use", recs[2].Entry.Type)
	assert.Equal(t, "read", recs[2].Entry.ToolName)
	assert.Contains(t, string(recs[2].Entry.ToolInput), "probe6.txt")

	require.NotNil(t, recs[3].Entry)
	assert.Equal(t, "tool_result", recs[3].Entry.Type)
	assert.Equal(t, "sixth probe file contents for tool-call capture", recs[3].Entry.ToolOutput)
	assert.False(t, recs[3].Entry.IsError)

	require.NotNil(t, recs[4].Entry)
	assert.Contains(t, recs[4].Entry.Content, "sixth probe file contents for tool-call capture")
}

// TestRecorder_EmptyInput_WritesNoFile pins the empty-input contract: a
// Recorder that never records anything must leave NO transcript file, not a
// zero-byte one masquerading as "captured, and empty" (memory
// "silent-no-op-failure-mode": ask of every writer whether empty input fails
// or succeeds writing nothing — this asserts the writer's actual choice).
func TestRecorder_EmptyInput_WritesNoFile(t *testing.T) {
	testsupport.Isolate(t)
	harp := "empty-harp"

	rec, err := NewRecorder(harp, "codex")
	require.NoError(t, err)
	require.NoError(t, rec.Close()) // never called Record

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "expected no transcript file for a session with zero recorded events, got err=%v", statErr)
}

// TestRecorder_RejectsEmptyChatEvent asserts a zero-value ChatEvent is
// rejected outright (no line written, seq not advanced) rather than recorded
// as a blank envelope.
func TestRecorder_RejectsEmptyChatEvent(t *testing.T) {
	testsupport.Isolate(t)
	harp := "reject-empty-harp"

	rec, err := NewRecorder(harp, "codex")
	require.NoError(t, err)

	err = rec.Record(agent.ChatEvent{})
	assert.Error(t, err)

	// A subsequent real event must still get seq 0 — the rejected call must
	// not have advanced anything or opened the file.
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type: agent.EntryTypeUser, Content: "hello",
	}}))
	require.NoError(t, rec.Close())

	recs := readRecordedLines(t, harp)
	require.Len(t, recs, 1)
	assert.Equal(t, 0, recs[0].Seq)
}

// TestRecorder_ConcurrentAppends_Safe drives many goroutines recording
// concurrently through one Recorder and asserts every event lands exactly
// once with a unique, gap-free seq — the "safe for concurrent appends"
// contract from the design.
func TestRecorder_ConcurrentAppends_Safe(t *testing.T) {
	testsupport.Isolate(t)
	harp := "concurrent-harp"

	rec, err := NewRecorder(harp, "codex")
	require.NoError(t, err)

	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:    agent.EntryTypeAssistant,
				Content: "concurrent",
			}})
		}(i)
	}
	wg.Wait()
	require.NoError(t, rec.Close())

	recs := readRecordedLines(t, harp)
	require.Len(t, recs, n)

	seen := make(map[int]bool, n)
	for _, r := range recs {
		assert.False(t, seen[r.Seq], "duplicate seq %d", r.Seq)
		seen[r.Seq] = true
	}
	for i := 0; i < n; i++ {
		assert.True(t, seen[i], "missing seq %d — concurrent appends dropped or gapped a line", i)
	}
}

// TestNewRecorder_ValidatesArgs pins the fail-loud-on-misuse contract: a blank
// harp or engine is a caller bug, rejected immediately rather than deferred
// to a confusing path-resolution failure later.
func TestNewRecorder_ValidatesArgs(t *testing.T) {
	testsupport.Isolate(t)

	_, err := NewRecorder("", "codex")
	assert.Error(t, err)

	_, err = NewRecorder("some-harp", "")
	assert.Error(t, err)
}

// TestTee_ForwardsAndRecords asserts Tee's two obligations: every event that
// goes in comes out unchanged on the returned channel, AND each one is
// recorded before being forwarded (so a slow/absent consumer never causes an
// event to be forwarded-but-unrecorded).
func TestTee_ForwardsAndRecords(t *testing.T) {
	testsupport.Isolate(t)
	harp := "tee-harp"

	rec, err := NewRecorder(harp, "codex")
	require.NoError(t, err)

	in := make(chan agent.ChatEvent)
	out := Tee(rec, in)

	go func() {
		defer close(in)
		in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "hi"}}
		in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "hello"}}
	}()

	var forwarded []agent.ChatEvent
	for ev := range out {
		forwarded = append(forwarded, ev)
	}
	require.NoError(t, rec.Close())

	require.Len(t, forwarded, 2)
	assert.Equal(t, "hi", forwarded[0].Entry.Content)
	assert.Equal(t, "hello", forwarded[1].Entry.Content)

	recs := readRecordedLines(t, harp)
	require.Len(t, recs, 2)
	assert.Equal(t, "hi", recs[0].Entry.Content)
	assert.Equal(t, "hello", recs[1].Entry.Content)
}
