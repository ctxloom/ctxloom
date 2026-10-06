package runner

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestEngineHost_DrivesNothingOnceClosed pins that a closed engine host
// refuses a StartRun rather than driving it.
//
// The runner tears its engine host down before its Home, and the Home's link
// joins (rather than drops) a request that was already on the wire — so a
// StartRun can reach the host after Close. Driven, it bound an identity,
// opened the run's transcript under HOME and handed off a first turn, all
// AFTER Close had joined the host's goroutines and closed its recorder: a
// run nobody would ever end, writing into a session its runner had finished
// with.
func TestEngineHost_DrivesNothingOnceClosed(t *testing.T) {
	closedHost := func(t *testing.T) (*EngineHost, *fakeEngineHome, *eventScript) {
		t.Helper()
		testsupport.Isolate(t)
		home := &fakeEngineHome{}
		sc := &eventScript{running: make(chan struct{})}
		eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
		eh.BindHome(home)
		eh.Close()
		return eh, home, sc
	}

	t.Run("a StartRun is refused", func(t *testing.T) {
		eh, home, sc := closedHost(t)
		// The runner tail's refuse seam doubles as the probe that it was
		// entered: a closed host must not even deliver the launch.
		var delivered atomic.Bool
		eh.BindRunner(testRunner{eh: eh, inst: sc, refuse: func() bool { delivered.Store(true); return false }})
		resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
		assert.Equal(t, int32(codes.FailedPrecondition), resp.GetStatus().GetCode(),
			"a StartRun reaching a closed engine host must be refused: %s", resp.GetStatus().GetMessage())
		assert.False(t, delivered.Load(), "a closed host handed the launch to its runner for delivery")
		assertNothingDriven(t, home)
	})

	// A launch whose delivery was already under way when Close began reaches
	// Drive past startRun's own check: Drive is where the refusal holds.
	t.Run("a launch already being delivered is not driven", func(t *testing.T) {
		eh, home, sc := closedHost(t)
		err := testRunner{eh: eh, inst: sc}.Execute(context.Background(), testStartRun("run-1").GetLaunch())
		require.ErrorIs(t, err, errEngineHostClosed)
		assertNothingDriven(t, home)
	})
}

// assertNothingDriven checks that no run was started: no identity bound, no
// event emitted, no transcript opened under HOME.
func assertNothingDriven(t *testing.T, home *fakeEngineHome) {
	t.Helper()
	home.mu.Lock()
	assert.Empty(t, home.identity.Harp, "a closed host bound the run's identity")
	assert.Empty(t, home.events, "a closed host emitted events for a run it will never end")
	home.mu.Unlock()

	path, err := paths.HarpCanonicalTranscriptPath("child-harp-1")
	require.NoError(t, err)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "a closed host opened the run's transcript under HOME (stat: %v)", statErr)
}

// heldHome holds Drive inside BindIdentity — the first thing it does past its
// closed check — until release closes.
type heldHome struct {
	*fakeEngineHome
	reached chan struct{}
	release chan struct{}
}

func (h *heldHome) BindIdentity(id coord.Identity) {
	close(h.reached)
	<-h.release
	h.fakeEngineHome.BindIdentity(id)
}

// TestEngineHost_CloseWaitsForADriveInFlight forces the residual window: Drive
// passes its closed check, THEN Close begins. Close must not seal and return
// while that Drive is still running — its later dispatches would be refused
// (or, before refusal, run unjoined) and it would open the run's transcript
// recorder after Close had closed it. Close waits for it, bounded.
func TestEngineHost_CloseWaitsForADriveInFlight(t *testing.T) {
	testsupport.Isolate(t)
	home := &heldHome{fakeEngineHome: &fakeEngineHome{}, reached: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(home.release) }) }
	t.Cleanup(release)

	sc := &eventScript{running: make(chan struct{})}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	eh.BindHome(home)

	driveErr := make(chan error, 1)
	go func() {
		driveErr <- testRunner{eh: eh, inst: sc}.Execute(context.Background(), testStartRun("run-1").GetLaunch())
	}()
	<-home.reached // Drive is past its closed check
	eh.mu.Lock()
	driving := eh.driving
	eh.mu.Unlock()
	require.NotNil(t, driving, "Drive reached BindIdentity without committing to the run")

	// The seam releases Drive only once Close is waiting for it, so a Close
	// that does not wait returns with Drive still held. Drive's return is
	// observed through driving (closed as Drive returns), not driveErr: the
	// goroutine sends driveErr only after Execute unwinds, which can trail a
	// Close that correctly waited.
	eh.closeAwaitsDrive = release
	eh.Close()

	select {
	case <-driving:
	default:
		t.Fatal("Close returned while a Drive was still in flight")
	}
	assert.NotErrorIs(t, <-driveErr, errEngineHostClosed, "Close sealed while Drive was in flight, so Drive's dispatches were refused")
	eh.mu.Lock()
	rec := eh.rec
	eh.mu.Unlock()
	assert.Nil(t, rec, "Drive's transcript recorder is open after Close returned")
}

// TestEngineHost_ADriveOutlastingCloseInstallsNoRecorder forces the window
// Close's bounded wait leaves: Drive passes its closed check, Close gives up
// waiting for it, seals and closes the recorder, and only THEN does Drive go
// on to open the run's recorder. Installed, that recorder would sit on a
// closed host that nothing ever closes again.
func TestEngineHost_ADriveOutlastingCloseInstallsNoRecorder(t *testing.T) {
	testsupport.Isolate(t)
	home := &heldHome{fakeEngineHome: &fakeEngineHome{}, reached: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(home.release) }) }
	t.Cleanup(release)

	sc := &eventScript{running: make(chan struct{})}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	eh.BindHome(home)

	driveErr := make(chan error, 1)
	go func() {
		driveErr <- testRunner{eh: eh, inst: sc}.Execute(context.Background(), testStartRun("run-1").GetLaunch())
	}()
	<-home.reached // Drive is past its closed check, held before it opens the recorder

	expired := make(chan time.Time)
	close(expired)
	eh.closeDriveExpired = expired // Close's wait for the held Drive runs out at once
	eh.Close()

	release()
	<-driveErr // Drive has returned: whatever it installs is installed

	eh.mu.Lock()
	rec := eh.rec
	eh.mu.Unlock()
	assert.Nil(t, rec, "a Drive that outlasted Close installed a transcript recorder nothing will close")
}
