package runner

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// lifecycleAt returns a channel that receives the fake's lifecycle as it
// stood at the instant the host reached what: the host calls atBoundary on
// its own goroutine, so the snapshot is ordered against everything the host
// did before it.
func lifecycleAt(home *fakeEngineHome, what string) <-chan []string {
	at := make(chan []string, 1)
	home.atBoundary = func(w string) {
		if w != what {
			return
		}
		home.mu.Lock()
		snap := slices.Clone(home.lifecycle)
		home.mu.Unlock()
		select {
		case at <- snap:
		default:
		}
	}
	return at
}

func receive(t *testing.T, at <-chan []string) []string {
	t.Helper()
	select {
	case got := <-at:
		return got
	case <-time.After(conformanceWait):
		require.FailNow(t, "the host never reached the boundary")
		return nil
	}
}

// TestEngineHost_AStructuredTurnsProcessExitIsAnnouncedBeforeItsBoundary: a
// structured launch runs one engine process per turn, and every MCP session
// that process opened on the endpoint dies with it. The host tells the home
// the process exited, once per turn and before the boundary — so the
// endpoint can close those sessions before the next turn's process can open
// any of its own.
func TestEngineHost_AStructuredTurnsProcessExitIsAnnouncedBeforeItsBoundary(t *testing.T) {
	home := &fakeEngineHome{}
	atIdle := lifecycleAt(home, "idle")
	eh := newTestEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	assert.Equal(t, []string{engineExitedMark}, receive(t, atIdle), "the turn's process exit is announced once, before its boundary")
}

// TestDrive_TheInteractiveEnginesExitIsAnnouncedBeforeTheRunExits: the
// interactive engine is one process for the whole run; its exit is announced
// before the run reports its own.
func TestDrive_TheInteractiveEnginesExitIsAnnouncedBeforeTheRunExits(t *testing.T) {
	home := &fakeEngineHome{}
	atExit := lifecycleAt(home, "exited")
	term := newFakeTerminal(0, nil)
	eh := newTestEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	eh.BindTerminal(term)
	require.NoError(t, eh.Drive(context.Background(), Turn{Launch: interactiveLaunch("harp-i", t.TempDir())}))

	close(term.release)
	assert.Equal(t, []string{engineExitedMark, "exited"}, receive(t, atExit))
}
