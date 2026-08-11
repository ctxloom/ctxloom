package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
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

// failingWriter always errors on Write — pins a fix: writeWatchText's text
// path used iox.ErrWriter's void-returning methods and never called w.Err(),
// so a failed write silently drained the rest of the event stream to nothing
// while `ctxloom session transcript watch` exited 0.
type failingWriter struct{ err error }

func (w *failingWriter) Write(p []byte) (int, error) { return 0, w.err }

// TestStreamWatchEvents_TextSurfacesWriteFailure pins a fix: a write
// failure partway through the text-mode feed must surface as an error from
// streamWatchEvents, not be silently swallowed by iox.ErrWriter's
// void-returning methods.
func TestStreamWatchEvents_TextSurfacesWriteFailure(t *testing.T) {
	events, errs := watchChan(entryEvent("assistant", "hello"))
	writeErr := errors.New("write broke")
	err := streamWatchEvents(&failingWriter{err: writeErr}, formatText, events, errs)
	require.Error(t, err, "a failed write in text mode must surface as an error, not exit 0 having drained the stream silently")
	assert.ErrorIs(t, err, writeErr)
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

// TestRunSessionWatch_ByLocation_RetiredScrapersErrorCleanly: a prior change
// deleted the by-location legacy-file readers for claude-code/codex/kiro
// outright (the user's DELETE decision, not a demoted reader —
// see each package's backend.go doc). A watch addressed by HARP whose only
// association is a located legacy-format transcript (no hook-bound session,
// no captured canonical transcript.jsonl — the containerized-child
// by-location shape this test used to successfully parse for these
// engines before that change) must now fail CLEANLY through
// operations.HistoryForBackend ("no session history") rather than hang,
// panic, or silently stream zero entries — matching the task's explicit
// acceptance that a retired-scraper backend with no canonical transcript
// "simply has no legacy reader" this release (interactive-pty/by-location
// memory for these three is scoped out to a later task). opencode is
// deliberately absent — its
// native reader was never file/path-addressable to begin with
// (GetSessionByPath always errored, see opencode/capabilities.go), so it was
// never covered by this by-location mechanism. antigravity was a fourth
// retired-scraper backend here until 0.7.0 removed the engine outright — a
// harp addressed to it now fails one gate earlier, with "unknown backend",
// since the backend itself no longer resolves; that is a different failure
// shape than this test pins, not the same one with a renamed message.
func TestRunSessionWatch_ByLocation_RetiredScrapersErrorCleanly(t *testing.T) {
	for _, backend := range []string{"claude-code", "codex", "kiro"} {
		t.Run(backend, func(t *testing.T) {
			home := testsupport.Isolate(t)
			harp := seedUnboundHarp(t, home, backend, "t.jsonl", "{}\n")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := &cobra.Command{}
			cmd.SetContext(ctx)
			cmd.SetOut(io.Discard)
			cmd.Flags().String("source", "auto", "") // as the real command registers it

			err := runSessionWatch(cmd, []string{harp})
			require.Error(t, err, "a retired scraper's by-location watch must fail loudly, not hang or stream nothing silently")
			assert.Contains(t, err.Error(), "no session history")
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
	cmd.Flags().String("source", "auto", "") // as the real command registers it

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
	cmd.Flags().String("source", "auto", "") // as the real command registers it

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

// fakeConsumerServer is a hermetic double for agentcoord.v1.ConsumerService
// (D1/D2) — just enough to drive `ctxloom session transcript watch`'s live path under
// test, the same level of realism the retired agentbus test used for its
// fake TapHub-backed bus server (see internal/operations/sessionfeed_test.go
// for the twin of this helper; duplicated rather than shared because the two
// live in different packages and this is the only test in this package that
// needs it).
type fakeConsumerServer struct {
	agentcoordpb.UnimplementedConsumerServiceServer
	mu   sync.Mutex
	runs []*agentcoordpb.ListRunsResult_RunInfo
	subs map[chan *agentcoordpb.AgentEvent]struct{}
}

func newFakeConsumerServer() *fakeConsumerServer {
	return &fakeConsumerServer{subs: map[chan *agentcoordpb.AgentEvent]struct{}{}}
}

func (f *fakeConsumerServer) ListRuns(context.Context, *agentcoordpb.ListRunsRequest) (*agentcoordpb.ListRunsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &agentcoordpb.ListRunsResult{Runs: f.runs}, nil
}

func (f *fakeConsumerServer) WatchRuns(_ *agentcoordpb.WatchRunsRequest, stream grpc.ServerStreamingServer[agentcoordpb.WatchEvent]) error {
	ch := make(chan *agentcoordpb.AgentEvent, 32)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}()
	if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Snapshot{Snapshot: &agentcoordpb.RosterSnapshot{}}}); err != nil {
		return err
	}
	for {
		select {
		case ev := <-ch:
			if err := stream.Send(&agentcoordpb.WatchEvent{Kind: &agentcoordpb.WatchEvent_Event{Event: ev}}); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (f *fakeConsumerServer) push(t *testing.T, ev *agentcoordpb.AgentEvent) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.subs) > 0
	}, 5*time.Second, 5*time.Millisecond, "no WatchRuns subscriber attached")
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		ch <- ev
	}
}

// startFakeCoordinator serves f over a real loopback gRPC listener and
// writes the endpoint.json internal/agentcoord/discover globs for — the D2
// discovery mechanism that replaced the retired agent-bus.sock convention.
func startFakeCoordinator(t *testing.T, home string, f *fakeConsumerServer) {
	t.Helper()
	srv := grpc.NewServer()
	agentcoordpb.RegisterConsumerServiceServer(srv, f)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)

	dir := filepath.Join(home, ".ctxloom", "coord", "proj")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body := fmt.Sprintf(`{"loopback_port":%d,"consumer_cred":"test-cred"}`, ln.Addr().(*net.TCPAddr).Port)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte(body), 0o600))
}

// TestRunSessionWatch_LiveTapE2E: the published command transparently gains
// the live source — a fake coordinator (D1 ConsumerService), discovered from
// ~/.ctxloom/coord/*/endpoint.json, streams NDJSON entries and boundaries as
// they happen, and the watch ends cleanly on RunCompleted.
func TestRunSessionWatch_LiveTapE2E(t *testing.T) {
	home := testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	f := newFakeConsumerServer()
	f.runs = []*agentcoordpb.ListRunsResult_RunInfo{{
		RunId: "run-1",
		Agent: &agentcoordpb.AgentIdentity{AgentId: entry.HarpName},
	}}
	startFakeCoordinator(t, home, f)

	out := &syncBuffer{}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(out)
	cmd.Flags().String("format", "json", "")
	cmd.Flags().String("source", "live", "")

	done := make(chan error, 1)
	go func() { done <- runSessionWatch(cmd, []string{entry.HarpName}) }()

	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{
		MessageId: "m-1", Role: agentcoordpb.MessageRole_MESSAGE_ROLE_ASSISTANT, Channel: agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL,
	}}})
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{
		MessageId: "m-1", Text: "live-hello",
	}}})
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_MessageCompleted{MessageCompleted: &agentcoordpb.MessageCompleted{
		MessageId: "m-1",
	}}})
	require.Eventually(t, func() bool {
		for _, e := range entryLines(out.String()) {
			if e.Content == "live-hello" {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "a live entry must reach the NDJSON stream")

	// "ctxloom/turn_idle" mirrors coord.CustomTurnIdle / operations'
	// customEventTurnIdle — a literal duplicate, documented on all three
	// sides (see internal/agentcoord/discover's file header for why this
	// package cannot import coord).
	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: "ctxloom/turn_idle"}}})
	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), `"boundary"`)
	}, 5*time.Second, 5*time.Millisecond, "turn_idle must arrive as a boundary")

	f.push(t, &agentcoordpb.AgentEvent{Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{}}})
	select {
	case werr := <-done:
		require.NoError(t, werr, "a live watch must end cleanly on RunCompleted")
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not end after RunCompleted")
	}
}

// TestWatchFeedSource pins the --source resolution `ctxloom session transcript watch`
// depends on. The lookup half matters as much as the parse half: a failed
// flag lookup used to be discarded, and ParseFeedSource reads the resulting ""
// as auto — so a user who asked for the live tap would silently get the store
// tail, which is a different feed (poll-and-diff over the recorded transcript,
// not the orchestrator's zero-lag event stream) and never ends when the
// child's engine exits.
func TestWatchFeedSource(t *testing.T) {
	newCmd := func(source string) *cobra.Command {
		c := &cobra.Command{}
		c.Flags().String("source", "auto", "")
		require.NoError(t, c.Flags().Set("source", source))
		return c
	}

	t.Run("registered values parse", func(t *testing.T) {
		for _, want := range []operations.FeedSource{operations.FeedSourceAuto, operations.FeedSourceLive, operations.FeedSourceStore} {
			got, err := watchFeedSource(newCmd(string(want)))
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}
	})

	t.Run("an unknown value is rejected", func(t *testing.T) {
		_, err := watchFeedSource(newCmd("tail"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown feed source")
	})

	t.Run("a failed lookup is an error, not a silent fallback to auto", func(t *testing.T) {
		_, err := watchFeedSource(&cobra.Command{}) // no --source registered
		require.Error(t, err, "a --source lookup that fails must not resolve to auto and quietly serve a different feed than the one asked for")
	})
}

// TestStreamWatchEvents_NDJSONSurfacesWriteFailure is the json-mode twin of
// TestStreamWatchEvents_TextSurfacesWriteFailure. `session transcript watch --format json`
// is the structured-frontend contract: a write failure that drained the feed
// and exited 0 would hand a consumer a silently truncated stream that is
// indistinguishable from a session that simply ended.
func TestStreamWatchEvents_NDJSONSurfacesWriteFailure(t *testing.T) {
	events, errs := watchChan(entryEvent("assistant", "hello"))
	writeErr := errors.New("write broke")
	err := streamWatchEvents(&failingWriter{err: writeErr}, formatJSON, events, errs)
	require.Error(t, err, "a failed write in json mode must surface as an error, not exit 0 having drained the stream silently")
	assert.ErrorIs(t, err, writeErr)
}
