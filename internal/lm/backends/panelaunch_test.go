package backends

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/tmuxhost"
)

// recordingRunner is a tmux that records instead of executing. It exists
// because tmux is NOT INSTALLED in this project's test environment, so the
// success arm below would otherwise be unrunnable -- and an unrunnable success
// arm is exactly how a hard dependency's happy path rots while the refusal arm
// keeps the suite green.
type recordingRunner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *recordingRunner) Run(_ context.Context, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), args...))
	return "", nil
}

func (r *recordingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingRunner) argvFor(sub string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == sub {
			return append([]string(nil), c...)
		}
	}
	return nil
}

// swapTmux points the launcher at a stub tmux lookup and runner for one test.
func swapTmux(t *testing.T, lookup func() (string, error), r tmuxhost.Runner) {
	t.Helper()
	oldLookup, oldRunner := resolveTmux, newPaneRunner
	resolveTmux = lookup
	newPaneRunner = func() tmuxhost.Runner { return r }
	t.Cleanup(func() { resolveTmux, newPaneRunner = oldLookup, oldRunner })
}

// TestInteractiveLaunch_WithoutTmuxRefusesLoudlyAndStartsNothing is the
// refusal arm. It asserts the EFFECT -- that no tmux command whatsoever was
// issued -- and not merely the message, because a refusal that still went on
// to start something would satisfy any assertion made about wording alone.
func TestInteractiveLaunch_WithoutTmuxRefusesLoudlyAndStartsNothing(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() { strictness.Reset(); strictness.SetDegraded(false) })

	r := &recordingRunner{}
	swapTmux(t, func() (string, error) {
		return "", errors.New(`exec: "tmux": executable file not found in $PATH`)
	}, r)

	code, err := RunLaunchSpec(context.Background(), agent.LaunchSpec{
		BinaryPath:  "/opt/engine/claude",
		Interactive: true,
		Harp:        "swift-amber-falcon",
	}, nil, io.Discard, io.Discard, nil)

	require.Error(t, err, "a missing hard dependency must not launch and must not be silent")
	assert.Contains(t, err.Error(), "tmux")
	assert.NotEqual(t, int32(0), code, "a refused launch must not report success")
	assert.Zero(t, r.count(), "NOTHING may be launched when tmux is missing: there is no pty fallback")

	// The refusal is reported as a finding for the startup choke to act on,
	// rather than this site deciding fatality for itself.
	found := strictness.All()
	require.Len(t, found, 1, "the refusal must be recorded, not only printed")
	assert.Equal(t, strictness.ClassConfig, found[0].Class)
	assert.Contains(t, strings.ToLower(found[0].Message), "tmux")
	assert.Contains(t, found[0].FixIt, "install tmux",
		"the remedy must name the fix, not merely state the fault")
}

// TestInteractiveLaunch_WithTmuxHostsTheEngineInAPane is the success arm: with
// tmux available, the ENGINE ITSELF is what ends up in the pane. Asserting the
// argv tmux receives rather than "Start returned nil" is deliberate -- a pane
// that hosted the wrong command, or hosted nothing, would still return nil.
func TestInteractiveLaunch_WithTmuxHostsTheEngineInAPane(t *testing.T) {
	r := &recordingRunner{}
	swapTmux(t, func() (string, error) { return "/usr/bin/tmux", nil }, r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RunLaunchSpec(ctx, agent.LaunchSpec{
			BinaryPath:  "/opt/engine/claude",
			Args:        []string{"--resume"},
			Env:         []string{"CTXLOOM_MARKER=pane-arm"},
			WorkDir:     "/w",
			Interactive: true,
			Harp:        "swift-amber-falcon",
			Engine:      "claude-code",
			Surface:     agent.CLISurfaceInteractive,
		}, nil, io.Discard, io.Discard, nil)
	}()

	require.Eventually(t, func() bool { return r.argvFor("new-window") != nil },
		5*time.Second, 5*time.Millisecond, "the engine must be hosted in a tmux window")
	cancel()
	<-done

	joined := strings.Join(r.argvFor("new-window"), " ")
	assert.Contains(t, joined, "/opt/engine/claude", "the pane must host the ENGINE binary")
	assert.Contains(t, joined, "--resume", "the engine's own args must reach the pane")
	assert.Contains(t, joined, "-c /w", "the run's working directory must reach the pane")
	assert.Contains(t, joined, "-e CTXLOOM_MARKER=pane-arm",
		"the merged environment must be passed explicitly: a tmux window otherwise inherits the shared server's env, not this run's")

	// Capture must be armed, or the pane would host the engine with nothing
	// relaying its output to the caller's terminal.
	assert.NotNil(t, r.argvFor("pipe-pane"), "the pane's output must be captured")
}
