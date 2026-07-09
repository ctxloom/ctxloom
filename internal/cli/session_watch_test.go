package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentbus"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// watchChan returns pre-filled, already-closed event/error channels modelling a
// completed observation feed.
func watchChan(events ...*pb.WatchEvent) (<-chan operations.SessionFeedEvent, <-chan error) {
	ec := make(chan operations.SessionFeedEvent, len(events))
	for _, e := range events {
		ec <- operations.SessionFeedEvent{Event: e}
	}
	close(ec)
	errc := make(chan error)
	close(errc)
	return ec, errc
}

// feedChan is watchChan for pre-wrapped feed events (gap markers included).
func feedChan(events ...operations.SessionFeedEvent) (<-chan operations.SessionFeedEvent, <-chan error) {
	ec := make(chan operations.SessionFeedEvent, len(events))
	for _, e := range events {
		ec <- e
	}
	close(ec)
	errc := make(chan error)
	close(errc)
	return ec, errc
}

func entryEvent(typ, content string) *pb.WatchEvent {
	return &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{Type: typ, Content: content}}}
}

func boundaryEvent(from, to int32) *pb.WatchEvent {
	return &pb.WatchEvent{Event: &pb.WatchEvent_Boundary{Boundary: &pb.ResponseBoundary{FromIndex: from, ToIndex: to}}}
}

func heartbeatEvent() *pb.WatchEvent {
	return &pb.WatchEvent{Event: &pb.WatchEvent_Heartbeat{Heartbeat: &pb.Heartbeat{}}}
}

// TestStreamWatchEvents_NDJSON: json mode emits one compact, valid JSON line per
// event in order, each discriminated by its oneof field name.
func TestStreamWatchEvents_NDJSON(t *testing.T) {
	events, errs := watchChan(entryEvent("assistant", "hi"), boundaryEvent(0, 1), heartbeatEvent())
	var buf bytes.Buffer

	require.NoError(t, streamWatchEvents(&buf, formatJSON, events, errs))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 3, "one NDJSON line per event")

	wantKey := []string{"entry", "boundary", "heartbeat"}
	for i, line := range lines {
		require.True(t, json.Valid([]byte(line)), "line %d must be valid JSON: %q", i, line)
		var obj map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(line), &obj))
		_, ok := obj[wantKey[i]]
		assert.True(t, ok, "line %d should carry %q; got %v", i, wantKey[i], obj)
	}
}

// TestStreamWatchEvents_Text: text mode prints turns, rules off boundaries, and
// stays silent on heartbeats.
func TestStreamWatchEvents_Text(t *testing.T) {
	events, errs := watchChan(entryEvent("assistant", "hi"), heartbeatEvent(), boundaryEvent(0, 1))
	var buf bytes.Buffer

	require.NoError(t, streamWatchEvents(&buf, formatText, events, errs))

	got := buf.String()
	assert.Equal(t, "assistant: hi\n"+watchBoundaryRule+"\n", got,
		"entry prints, heartbeat is silent, boundary draws the rule")
}

// TestStreamWatchEvents_TextToolEntries: tool turns render compactly with a
// success/error marker.
func TestStreamWatchEvents_TextToolEntries(t *testing.T) {
	use := &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{Type: "tool_use", ToolName: "Bash"}}}
	okRes := &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{Type: "tool_result", ToolName: "Bash"}}}
	errRes := &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{Type: "tool_result", ToolName: "Bash", IsError: true}}}
	events, errs := watchChan(use, okRes, errRes)
	var buf bytes.Buffer

	require.NoError(t, streamWatchEvents(&buf, formatText, events, errs))
	assert.Equal(t, "  → Bash\n  ✓ Bash\n  ✗ Bash\n", buf.String())
}

// TestStreamWatchEvents_NDJSONCarriesSidechain: the subagent-attribution flag
// must reach NDJSON consumers, so a viewer can tell interior entries from the
// main thread.
func TestStreamWatchEvents_NDJSONCarriesSidechain(t *testing.T) {
	side := &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{
		Type: "assistant", Content: "interior", Sidechain: true,
	}}}
	events, errs := watchChan(side)
	var buf bytes.Buffer

	require.NoError(t, streamWatchEvents(&buf, formatJSON, events, errs))

	var line struct {
		Entry struct {
			Type      string `json:"type"`
			Content   string `json:"content"`
			Sidechain bool   `json:"sidechain"`
		} `json:"entry"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line))
	assert.Equal(t, "assistant", line.Entry.Type)
	assert.Equal(t, "interior", line.Entry.Content)
	assert.True(t, line.Entry.Sidechain)
}

// TestStreamWatchEvents_TextSidechainPrefix: text mode prefixes
// subagent-interior entries with "↳" so a human can tell them apart.
func TestStreamWatchEvents_TextSidechainPrefix(t *testing.T) {
	sideText := &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{
		Type: "assistant", Content: "interior", Sidechain: true,
	}}}
	sideTool := &pb.WatchEvent{Event: &pb.WatchEvent_Entry{Entry: &pb.SessionEntry{
		Type: "tool_use", ToolName: "Grep", Sidechain: true,
	}}}
	events, errs := watchChan(sideText, sideTool)
	var buf bytes.Buffer

	require.NoError(t, streamWatchEvents(&buf, formatText, events, errs))
	assert.Equal(t, "↳ assistant: interior\n  ↳ → Grep\n", buf.String())
}

// TestStreamWatchEvents_PropagatesStreamError: a fatal mid-stream error surfaces
// after the events drain.
func TestStreamWatchEvents_PropagatesStreamError(t *testing.T) {
	ec := make(chan operations.SessionFeedEvent)
	close(ec)
	errc := make(chan error, 1)
	errc <- errors.New("stream broke")
	close(errc)

	err := streamWatchEvents(io.Discard, formatText, ec, errc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream broke")
}

// TestStreamWatchEvents_UnknownFormat rejects an unsupported --format.
func TestStreamWatchEvents_UnknownFormat(t *testing.T) {
	events, errs := watchChan()
	err := streamWatchEvents(io.Discard, "yaml", events, errs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

// TestStreamWatchEvents_GapMarker: a live-source gap renders as its own NDJSON
// line kind (a viewer must know it missed events) and as an explicit text note.
func TestStreamWatchEvents_GapMarker(t *testing.T) {
	events, errs := feedChan(
		operations.SessionFeedEvent{Gap: 3},
		operations.SessionFeedEvent{Event: entryEvent("assistant", "after the gap")},
	)
	var buf bytes.Buffer
	require.NoError(t, streamWatchEvents(&buf, formatJSON, events, errs))
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, `{"gap":{"dropped":3}}`, lines[0])
	assert.Contains(t, lines[1], "after the gap")

	events, errs = feedChan(operations.SessionFeedEvent{Gap: 3})
	buf.Reset()
	require.NoError(t, streamWatchEvents(&buf, formatText, events, errs))
	assert.Contains(t, buf.String(), "missed 3 live events")
}

// --- by-harp resolution through by-location discovery ---

// syncBuffer is a goroutine-safe writer the watch goroutine streams into while
// the test polls for output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// watchEntry is the NDJSON entry shape the by-location tests decode.
type watchEntry struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	ToolName  string `json:"toolName"`
	Sidechain bool   `json:"sidechain"`
}

// entryLines decodes the entry events among the NDJSON lines written so far,
// ignoring boundaries/heartbeats and any trailing partial line.
func entryLines(out string) []watchEntry {
	var entries []watchEntry
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasSuffix(line, "}") { // partial or empty
			continue
		}
		var ev struct {
			Entry *watchEntry `json:"entry"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Entry != nil {
			entries = append(entries, *ev.Entry)
		}
	}
	return entries
}

// seedUnboundHarp creates an index entry whose bind hook never fired (no
// session id, no transcript path) and drops a fixture transcript at rel under
// the harp's persist/transcripts store — the containerized-child shape that
// by-location discovery resolves.
func seedUnboundHarp(t *testing.T, home, backend, rel, fixture string) string {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", backend)
	require.NoError(t, err)

	p := filepath.Join(home, ".ctxloom", "sessions", entry.HarpName,
		"persist", "transcripts", filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(fixture), 0o644))
	return entry.HarpName
}

// watchHarpNDJSON runs runSessionWatch on the harp in json mode, waits until
// wantEntries entry lines arrived, cancels the watch, and returns the entries.
func watchHarpNDJSON(t *testing.T, harp string, wantEntries int) []watchEntry {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := &syncBuffer{}
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(out)
	cmd.Flags().String("format", "json", "")

	done := make(chan error, 1)
	go func() { done <- runSessionWatch(cmd, []string{harp}) }()

	require.Eventually(t, func() bool {
		return len(entryLines(out.String())) >= wantEntries
	}, 5*time.Second, 5*time.Millisecond)

	cancel()
	require.NoError(t, <-done, "a cancelled watch must end cleanly")
	return entryLines(out.String())
}

// TestRunSessionWatch_ByLocationAcrossBackends: a watch addressed by HARP
// resolves the right transcript for each history-backed backend through the
// by-location discovery path — an index entry with no hook-bound session,
// whose transcript lives in the harp's persist/ store under the engine's own
// nested layout — and streams its normalized entries hermetically (no live
// engines, no plugins: the by-path tail parses in-process).
func TestRunSessionWatch_ByLocationAcrossBackends(t *testing.T) {
	cases := []struct {
		backend string
		rel     string // engine-native nesting inside persist/transcripts
		fixture string
		want    int
		check   func(t *testing.T, entries []watchEntry)
	}{
		{
			backend: "claude-code",
			rel:     "-proj-enc/uuid-1.jsonl",
			fixture: `{"type":"user","timestamp":"2026-06-01T10:00:01Z","message":{"role":"user","content":"hi from claude"}}
{"type":"assistant","isSidechain":true,"timestamp":"2026-06-01T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"interior probe"}]}}
{"type":"assistant","timestamp":"2026-06-01T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"claude answer"}]}}`,
			want: 3,
			check: func(t *testing.T, entries []watchEntry) {
				assert.Equal(t, "user", entries[0].Type)
				assert.Equal(t, "hi from claude", entries[0].Content)
				assert.False(t, entries[0].Sidechain)
				assert.True(t, entries[1].Sidechain, "subagent-interior entry must arrive attributed")
				assert.Equal(t, "interior probe", entries[1].Content)
				assert.Equal(t, "claude answer", entries[2].Content)
				assert.False(t, entries[2].Sidechain)
			},
		},
		{
			backend: "codex",
			rel:     "rollout-2026-05-27.jsonl",
			fixture: `{"type":"message","role":"user","content":"hi codex","timestamp":"2026-05-27T10:00:00Z"}
{"type":"message","role":"assistant","content":"codex answer","timestamp":"2026-05-27T10:00:05Z"}`,
			want: 2,
			check: func(t *testing.T, entries []watchEntry) {
				assert.Equal(t, "user", entries[0].Type)
				assert.Equal(t, "hi codex", entries[0].Content)
				assert.Equal(t, "assistant", entries[1].Type)
				assert.Equal(t, "codex answer", entries[1].Content)
			},
		},
		{
			backend: "kiro",
			rel:     "sess-1.jsonl",
			fixture: `{"role":"user","content":"hi kiro"}
{"role":"assistant","content":[{"type":"text","text":"kiro answer"}]}`,
			want: 2,
			check: func(t *testing.T, entries []watchEntry) {
				assert.Equal(t, "user", entries[0].Type)
				assert.Equal(t, "hi kiro", entries[0].Content)
				assert.Equal(t, "assistant", entries[1].Type)
				assert.Equal(t, "kiro answer", entries[1].Content)
			},
		},
		{
			backend: "antigravity",
			rel:     "uuid-1/.system_generated/logs/transcript_full.jsonl",
			fixture: `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-10T20:05:07Z","content":"<USER_REQUEST>\nhi agy\n</USER_REQUEST>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-10T20:05:10Z","content":"agy answer"}`,
			want: 2,
			check: func(t *testing.T, entries []watchEntry) {
				assert.Equal(t, "user", entries[0].Type)
				assert.Contains(t, entries[0].Content, "hi agy")
				assert.Equal(t, "assistant", entries[1].Type)
				assert.Equal(t, "agy answer", entries[1].Content)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.backend, func(t *testing.T) {
			home := testsupport.Isolate(t)
			harp := seedUnboundHarp(t, home, tc.backend, tc.rel, tc.fixture)
			entries := watchHarpNDJSON(t, harp, tc.want)
			require.GreaterOrEqual(t, len(entries), tc.want)
			tc.check(t, entries[:tc.want])
		})
	}
}

// TestRunSessionWatch_NothingToWatch: a harp with no bound session and no
// located transcript is a clear error, not a silent empty stream.
func TestRunSessionWatch_NothingToWatch(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)

	err = runSessionWatch(cmd, []string{entry.HarpName})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to watch")
}

// TestRunSessionWatch_UnknownBackend: a by-location entry whose backend isn't
// registered fails loudly instead of watching with the wrong parser.
func TestRunSessionWatch_UnknownBackend(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := seedUnboundHarp(t, home, "no-such-engine", "t.jsonl", "{}\n")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)

	err := runSessionWatch(cmd, []string{harp})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown backend")
}

// TestRunSessionWatch_UnknownSource: an invalid --source is rejected before
// any resolution.
func TestRunSessionWatch_UnknownSource(t *testing.T) {
	testsupport.Isolate(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	cmd.Flags().String("source", "psychic", "")

	err := runSessionWatch(cmd, []string{"whatever-harp"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown feed source")
}

// --- live source through the published command ---

// TestRunSessionWatch_LiveTapE2E: the published command transparently gains
// the live source — a fake orchestrator-held child, observed over a real bus
// socket discovered from the environment, streams NDJSON entries and
// boundaries as they happen, and the watch ends cleanly when the child's
// stream closes.
func TestRunSessionWatch_LiveTapE2E(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	hub := agentbus.NewTapHub()
	sock := filepath.Join(t.TempDir(), "bus.sock")
	srv, err := agentbus.Listen(sock, agentbus.New(agentbus.Hooks{}), hub, nil, nil)
	require.NoError(t, err)
	defer srv.Close()
	t.Setenv(agentbus.SocketEnv, sock)

	// The fake orchestrator: holds the child's stream, consuming at its own
	// pace, while the watch taps it.
	in := make(chan agent.ChatEvent)
	consumed := hub.Tee(entry.HarpName, in)
	go func() {
		for range consumed {
		}
	}()

	out := &syncBuffer{}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(out)
	cmd.Flags().String("format", "json", "")
	cmd.Flags().String("source", "live", "")

	done := make(chan error, 1)
	go func() { done <- runSessionWatch(cmd, []string{entry.HarpName}) }()

	// The watch's subscription races the first push (live taps deliver from
	// subscribe-time forward), so emit entries until one lands in the output.
	pusherStop := make(chan struct{})
	pusherDone := make(chan struct{})
	go func() {
		defer close(pusherDone)
		for {
			select {
			case in <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "live-hello"}}:
			case <-pusherStop:
				return
			}
			select {
			case <-time.After(10 * time.Millisecond):
			case <-pusherStop:
				return
			}
		}
	}()
	require.Eventually(t, func() bool {
		for _, e := range entryLines(out.String()) {
			if e.Content == "live-hello" {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "a live entry must reach the NDJSON stream")
	close(pusherStop)
	<-pusherDone

	in <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), `"boundary"`)
	}, 5*time.Second, 5*time.Millisecond, "the turn Complete must arrive as a boundary")

	close(in) // the child's engine exits → the live watch ends on its own
	select {
	case werr := <-done:
		require.NoError(t, werr, "a live watch must end cleanly when the child ends")
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not end after the child's stream closed")
	}
}
