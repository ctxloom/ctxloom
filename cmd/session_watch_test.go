package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// watchChan returns pre-filled, already-closed event/error channels modelling a
// completed WatchSession stream.
func watchChan(events ...*pb.WatchEvent) (<-chan *pb.WatchEvent, <-chan error) {
	ec := make(chan *pb.WatchEvent, len(events))
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

// TestStreamWatchEvents_PropagatesStreamError: a fatal mid-stream error surfaces
// after the events drain.
func TestStreamWatchEvents_PropagatesStreamError(t *testing.T) {
	ec := make(chan *pb.WatchEvent)
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
