package coord

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// testHome builds a Home with no coordinator connection: the emission paths
// buffer into unacked and the nil stream drops the send, which is exactly the
// state a runner is in between reconnects.
func testHome(t *testing.T) *Home {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Home{
		ctx:         ctx,
		cancel:      cancel,
		ackCh:       make(chan struct{}),
		pending:     make(map[string]*homeReq),
		consumed:    make(map[string]bool),
		turnPending: make(map[string]bool),
	}
}

// captureWarnings redirects clidiag's process-wide sink for the test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}

// emitted copies the events Home has buffered as unacked.
func (h *Home) emitted() []*agentcoordpb.AgentEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*agentcoordpb.AgentEvent(nil), h.unacked...)
}

// TestReport_EmptyReportIsRefused: a Report carrying neither a summary nor any
// artifact must fail loudly. A nil return with zero events emitted is the
// exit-0-zero-bytes shape — the caller's `if err == nil` reads it as "filed and
// durable" when nothing was filed at all.
func TestReport_EmptyReportIsRefused(t *testing.T) {
	h := testHome(t)

	err := h.Report(context.Background(), nil, nil)

	require.Error(t, err, "an empty report must not answer success")
	assert.Empty(t, h.emitted(), "nothing was emitted, so nothing can be reported as filed")
}

// TestReport_ArtifactOnlyStillFilesAndWaits: only the empty case is refused —
// an artifact-only report (no summary) is a legitimate filing.
func TestReport_ArtifactOnlyStillFiles(t *testing.T) {
	h := testHome(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // no coordinator to ack: the wait ends immediately
	_ = h.Report(ctx, nil, []*agentcoordpb.ArtifactProduced{{ArtifactId: "a-1"}})

	events := h.emitted()
	require.Len(t, events, 1, "the artifact manifest must still be emitted")
	assert.Equal(t, "a-1", events[0].GetArtifactProduced().GetArtifactId())
}

// TestEmitCustomEvent_UnencodableValueIsNotEmittedAsValueless: mail_consumed
// carries the consumption cursor in its value. Swallowing the encode error and
// emitting the event anyway spends a seq on an event whose payload says
// "consumed nothing" — the coordinator's cursor never advances and nobody is
// told. Dropping it instead leaves the messages unacked, which re-delivers
// (the safe direction), and warns.
func TestEmitCustomEvent_UnencodableValueIsNotEmittedAsValueless(t *testing.T) {
	h := testHome(t)
	warnings := captureWarnings(t)

	h.emitCustomEvent(CustomMailConsumed, map[string]any{"message_ids": make(chan int)})

	assert.Empty(t, h.emitted(), "an event whose value could not be encoded must not be emitted at all")
	assert.Contains(t, warnings.String(), CustomMailConsumed, "the dropped event must be named on stderr")
}

// TestEmitCustomEvent_NilValueStillEmits: the park/unpark assertions carry no
// value by design and must keep emitting.
func TestEmitCustomEvent_NilValueStillEmits(t *testing.T) {
	h := testHome(t)

	h.emitCustomEvent(CustomRecvParked, nil)

	events := h.emitted()
	require.Len(t, events, 1)
	assert.Equal(t, CustomRecvParked, events[0].GetCustom().GetName())
	assert.Nil(t, events[0].GetCustom().GetValue())
}

// TestSetTurnSink_SecondRegistrationIsAnnounced: one Home hosts one run, so a
// second sink is a wiring bug. Refusing it silently left the new engine
// waiting for turns that were being handed to the old sink, with nothing on
// any channel to say so.
func TestSetTurnSink_SecondRegistrationIsAnnounced(t *testing.T) {
	h := testHome(t)
	warnings := captureWarnings(t)

	first := make(chan string, 4)
	h.SetTurnSink(func(*agentcoordpb.PeerMessage) bool { first <- "first"; return true })
	h.SetTurnSink(func(*agentcoordpb.PeerMessage) bool { return true })

	assert.Contains(t, warnings.String(), "turn sink", "the refused registration must be reported")
}
