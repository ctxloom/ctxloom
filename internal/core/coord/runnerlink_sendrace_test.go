package coord

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// A CONFIRMED DATA RACE, pinned at the seam that caused it.
//
// gRPC's ClientStream permits exactly one concurrent reader and one concurrent
// writer. CloseSend is a WRITER: it writes the stream's own sent-last state,
// which SendMsg reads and writes too. RunnerLink.Shutdown called it
// unserialized while the heartbeat loop was still ticking, and the detector
// caught that pair — CloseSend under Shutdown against SendMsg under
// heartbeatLoop — during another branch's merge gate.
//
// The pin below does NOT depend on the detector or on winning a timing race:
// the stub stream reports, deterministically, whether CloseSend was entered
// while a send was in flight. Reverting either half of the fix (the mutex, or
// the closed-flag) turns it red.

// serialStream is a stub RunnerChannel stream that records whether its
// send-side operations were ever CONCURRENT.
//
// It counts occupants rather than timing anything: any operation that finds
// another already inside is an overlap, which is precisely the gRPC contract
// violation under test. Send blocks until released, so the window is opened by
// the test rather than hoped for.
type serialStream struct {
	release chan struct{} // Send returns only once this is closed
	entered chan struct{} // closed when the first Send is inside

	mu           sync.Mutex
	occupants    int
	overlaps     int
	closeSendSaw int // occupants CloseSend found already inside
	sends        int
	closes       int
	enteredOnce  sync.Once
}

func newSerialStream() *serialStream {
	return &serialStream{release: make(chan struct{}), entered: make(chan struct{})}
}

// enter records one more occupant and reports how many were already there.
func (s *serialStream) enter() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.occupants
	s.occupants++
	if before > 0 {
		s.overlaps++
	}
	return before
}

func (s *serialStream) leave() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.occupants--
}

func (s *serialStream) Send(*agentcoordpb.RunnerFrame) error {
	s.enter()
	defer s.leave()
	s.mu.Lock()
	s.sends++
	s.mu.Unlock()
	s.enteredOnce.Do(func() { close(s.entered) })
	<-s.release
	return nil
}

func (s *serialStream) CloseSend() error {
	before := s.enter()
	defer s.leave()
	s.mu.Lock()
	s.closes++
	s.closeSendSaw += before
	s.mu.Unlock()
	return nil
}

func (s *serialStream) stats() (overlaps, closeSendSaw, sends, closes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overlaps, s.closeSendSaw, s.sends, s.closes
}

// The rest of the BidiStreamingClient surface, unused by this test: Recv
// blocks until the link is torn down, which is what the real receive loop
// does.
func (s *serialStream) Recv() (*agentcoordpb.RuntimeFrame, error) {
	<-s.release
	return nil, context.Canceled
}
func (s *serialStream) Header() (metadata.MD, error) { return nil, nil }
func (s *serialStream) Trailer() metadata.MD         { return nil }
func (s *serialStream) Context() context.Context     { return context.Background() }
func (s *serialStream) SendMsg(any) error            { return nil }
func (s *serialStream) RecvMsg(any) error            { return nil }

// linkWithStubStream builds a RunnerLink over the stub, with the lazy client
// connection Shutdown closes at the end. grpc.NewClient does not dial, so no
// server is involved and nothing here can hang on the network.
func linkWithStubStream(t *testing.T, runID string, s *serialStream) *RunnerLink {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///127.0.0.1:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	_, cancel := context.WithCancel(context.Background())
	l := &RunnerLink{
		runID:  runID,
		conn:   conn,
		stream: s,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	close(l.done) // no receive loop in this fixture; Shutdown's wait is satisfied
	return l
}

// closeSendWatchWindow bounds how long the test waits to see whether CloseSend
// runs while a send is in flight.
//
// Its EXPIRY is the fixed behaviour — CloseSend is parked on the send mutex,
// which is the whole point — so the window is an observation budget, never a
// synchronisation device: the assertion below reads the stub's record, so a
// CloseSend that slipped through late is still caught after the release.
const closeSendWatchWindow = 250 * time.Millisecond

// TestRunnerLink_ShutdownNeverClosesSendWhileASendIsInFlight is the race's own
// pin, in the shape that reproduced it: a link whose send side is busy when
// Shutdown reaches its half-close.
//
// runID is EMPTY on purpose. That is the session-owner runner's real
// configuration (Shutdown's own comment: "a session-owner runner has no run to
// report exited"), and it is what makes the window deterministic rather than
// lucky — with a run id, Shutdown's own RunExited send takes the mutex first
// and the collision depends on a heartbeat landing in the gap after it.
func TestRunnerLink_ShutdownNeverClosesSendWhileASendIsInFlight(t *testing.T) {
	s := newSerialStream()
	l := linkWithStubStream(t, "", s)

	// A sender in flight — the heartbeat's position in the real defect.
	sendDone := make(chan error, 1)
	go func() { sendDone <- l.send(&agentcoordpb.RunnerFrame{}) }()
	select {
	case <-s.entered:
	case <-time.After(conformanceWait):
		t.Fatal("the stub's send was never entered")
	}

	shutdownDone := make(chan struct{})
	go func() { defer close(shutdownDone); l.Shutdown(0, "") }()

	// Give Shutdown every chance to reach its half-close. Under the defect it
	// takes it immediately; under the fix it is parked on the send mutex.
	select {
	case <-shutdownDone:
	case <-time.After(closeSendWatchWindow):
	}

	close(s.release)
	select {
	case <-shutdownDone:
	case <-time.After(conformanceWait):
		t.Fatal("Shutdown never completed after the send was released")
	}
	require.NoError(t, <-sendDone)

	overlaps, closeSendSaw, sends, closes := s.stats()
	require.Equal(t, 1, sends, "the fixture's own send must have happened, or this test proves nothing")
	require.Equal(t, 1, closes, "Shutdown must still half-close the stream: the graceful end is not what we are giving up")
	assert.Zero(t, closeSendSaw,
		"CloseSend ran while a send was in flight — the exact gRPC contract violation the detector caught "+
			"(one reader and one writer are allowed; two writers are a race inside the stream's own state)")
	assert.Zero(t, overlaps, "no two send-side operations may ever be inside the stream at once")
}

// TestRunnerLink_SendAfterHalfCloseIsRefusedByUs pins the other half of the
// fix, and it needs no timing at all: once the send side is closed, a sender
// that acquires the mutex afterwards must be refused HERE.
//
// Without the flag that sender reaches gRPC, which answers a SendMsg after
// CloseSend with codes.Internal — a caller bug by gRPC's own classification,
// reading in the log as a library fault during what is actually an orderly
// shutdown.
func TestRunnerLink_SendAfterHalfCloseIsRefusedByUs(t *testing.T) {
	s := newSerialStream()
	close(s.release) // nothing blocks in this half of the pin
	l := linkWithStubStream(t, "", s)

	l.closeSend()
	err := l.send(&agentcoordpb.RunnerFrame{})
	require.Error(t, err, "a frame written after the half-close must be refused, not handed to a closed send side")
	assert.ErrorIs(t, err, ErrLinkSendClosed)

	_, _, sends, closes := s.stats()
	assert.Zero(t, sends, "the refused frame must never have reached the stream")
	assert.Equal(t, 1, closes)

	// Idempotent: a second half-close is not a second wire operation.
	l.closeSend()
	_, _, _, closes = s.stats()
	assert.Equal(t, 1, closes, "the half-close must happen once, whatever calls it")
}
