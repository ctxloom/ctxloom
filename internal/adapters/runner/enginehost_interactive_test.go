package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// fakeTerminal is a Terminal double: it records the turn it was handed, holds
// the "engine" open until released, and exits with the code it was given.
type fakeTerminal struct {
	mu      sync.Mutex
	turns   []Turn
	release chan struct{}
	code    int
	err     error
}

func newFakeTerminal(code int, err error) *fakeTerminal {
	return &fakeTerminal{release: make(chan struct{}), code: code, err: err}
}

func (f *fakeTerminal) Run(ctx context.Context, t Turn) (int, error) {
	f.mu.Lock()
	f.turns = append(f.turns, t)
	f.mu.Unlock()
	select {
	case <-f.release:
		return f.code, f.err
	case <-ctx.Done():
		return 1, ctx.Err()
	}
}

func (f *fakeTerminal) seen() []Turn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Turn(nil), f.turns...)
}

func interactiveLaunch(harp, workDir string) launch.Launch {
	l := launchtest.Structured(harp, "claude-code", "lbl", "m", workDir, agent.PermissionDefault)
	l.Mode = engine.Interactive
	return l
}

// TestDrive_InteractiveLaunchDrivesTheTerminalNotTheChat: an INTERACTIVE
// launch is driven on the runner's own terminal — the pty slave the
// originator holds the master of, or the container's -it tty — through the
// Terminal the composition root bound. The structured chat is never started
// for it, no turn sink is registered (coordinator mail reaches an
// interactive engine through the terminal injector's nudge, not as a queued
// turn), and the engine's exit is the run's terminal: RunCompleted then
// RunExited with the engine's exit code.
func TestDrive_InteractiveLaunchDrivesTheTerminalNotTheChat(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	term := newFakeTerminal(0, nil)
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	eh.BindTerminal(term)

	presented := []present.Presentation{{HostPath: "/h/mcp.json", EnginePath: "/e/mcp.json", Args: []string{"--mcp-config", "/e/mcp.json"}}}
	err := eh.Drive(context.Background(), Turn{Launch: interactiveLaunch("harp-i", t.TempDir()), Presented: presented})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(term.seen()) == 1 }, conformanceWait, 10*time.Millisecond, "the terminal must be driven")
	assert.Equal(t, presented, term.seen()[0].Presented, "the terminal drive execs over what delivery produced")
	assert.Empty(t, sc.RecordedTexts(), "no structured chat for an interactive launch")
	home.mu.Lock()
	sink := home.sink
	home.mu.Unlock()
	assert.Nil(t, sink, "an interactive run registers no turn sink: mail reaches it through the terminal")
	assert.Contains(t, home.payloadKinds(), "run_started")
	assert.NotContains(t, home.payloadKinds(), "run_completed", "the run is live while the engine holds the terminal")

	close(term.release)
	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.exited) == 1
	}, conformanceWait, 10*time.Millisecond)
	home.mu.Lock()
	defer home.mu.Unlock()
	assert.Equal(t, 0, home.exited[0].Code)
	var completed *agentcoordpb.RunCompleted
	for _, ev := range home.events {
		if rc, ok := ev.GetPayload().(*agentcoordpb.AgentEvent_RunCompleted); ok {
			completed = rc.RunCompleted
		}
	}
	require.NotNil(t, completed, "the engine's exit is the run's terminal")
	assert.Equal(t, agentcoordpb.Result_RUN_STATUS_SUCCEEDED, completed.GetResult().GetStatus())
}

// TestDrive_InteractiveEngineFailureIsTheRunsFailure: a non-zero exit, or a
// terminal that could not run the engine at all, ends the run FAILED with
// that code — never a green terminal over an engine that died.
func TestDrive_InteractiveEngineFailureIsTheRunsFailure(t *testing.T) {
	home := &fakeEngineHome{}
	term := newFakeTerminal(3, errors.New("tmux was not found"))
	eh := newTestEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	eh.BindTerminal(term)
	require.NoError(t, eh.Drive(context.Background(), Turn{Launch: interactiveLaunch("harp-f", t.TempDir())}))
	close(term.release)
	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.exited) == 1
	}, conformanceWait, 10*time.Millisecond)
	home.mu.Lock()
	defer home.mu.Unlock()
	assert.Equal(t, 3, home.exited[0].Code)
	for _, ev := range home.events {
		if rc, ok := ev.GetPayload().(*agentcoordpb.AgentEvent_RunCompleted); ok {
			assert.Equal(t, agentcoordpb.Result_RUN_STATUS_FAILED, rc.RunCompleted.GetResult().GetStatus())
			assert.Contains(t, rc.RunCompleted.GetResult().GetError().GetMessage(), "tmux was not found")
		}
	}
}

// TestDrive_InteractiveLaunchWithoutATerminalIsRefused: a runner composed
// without a terminal (a structured-only composition) refuses an interactive
// launch by name rather than driving it through the chat.
func TestDrive_InteractiveLaunchWithoutATerminalIsRefused(t *testing.T) {
	home := &fakeEngineHome{}
	eh := newTestEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	err := eh.Drive(context.Background(), Turn{Launch: interactiveLaunch("harp-n", t.TempDir())})
	require.ErrorIs(t, err, ErrNoTerminal)
}
