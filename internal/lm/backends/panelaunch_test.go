package backends

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/tmuxhost"
)

// recordingRunner is a tmux that records instead of executing.
//
// It exists so the success arm below runs ANYWHERE. That is the property a
// test of the argv we hand tmux should have: what is under test is what THIS
// package sends, and that claim is true whether or not a tmux is installed to
// receive it. Gating it on a real binary is how a hard dependency's happy path
// rots unnoticed while the refusal arm keeps the suite green.
//
// It does not replace a real-tmux test. Only a real tmux can prove tmux
// ACCEPTS the argv; this proves we built the argv we meant to, unconditionally.
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
			BinaryPath: "/opt/engine/claude",
			Args:       []string{"--resume"},
			// Duplicated deliberately: BuildEnv produces os.Environ() first
			// and the caller's overrides last, so LAST MUST WIN. A conversion
			// that kept the first occurrence would silently revert every
			// override to the ambient value -- the engine would run with the
			// wrong config and nothing would look wrong.
			Env:         []string{"CTXLOOM_MARKER=ambient-stale", "CTXLOOM_MARKER=pane-arm"},
			WorkDir:     "/w",
			Interactive: true,
			Harp:        "swift-amber-falcon",
			Engine:      "claude-code",
			Surface:     agent.CLISurfaceInteractive,
		}, nil, io.Discard, io.Discard, nil)
	}()

	require.Eventually(t, func() bool { return r.argvFor("new-window") != nil },
		5*time.Second, 5*time.Millisecond, "the engine must be hosted in a tmux window")

	joined := strings.Join(r.argvFor("new-window"), " ")

	// The engine, its args and the merged environment reach the pane through a
	// LAUNCHER FILE rather than this command line, which tmux caps and which
	// would otherwise grow with both the environment and the engine's prompt
	// (see tmuxhost.writeLauncher). Read it BEFORE cancelling: the pane's temp
	// directory is reclaimed when the run's context ends, so a read after the
	// teardown below finds nothing and reports it as a missing launcher.
	body, err := os.ReadFile(launcherPathFrom(t, joined))
	require.NoError(t, err, "the pane's launcher must exist")
	script := string(body)

	cancel()
	<-done

	assert.Contains(t, joined, "-c /w", "the run's working directory must reach the pane")
	// Assert the command line is FREE of the environment — that absence is the
	// property that stops tmux's "command too long" coming back.
	assert.NotContains(t, joined, "-e CTXLOOM_MARKER", "no environment may ride the tmux command line")

	assert.Contains(t, script, "/opt/engine/claude", "the pane must host the ENGINE binary")
	assert.Contains(t, script, "--resume", "the engine's own args must reach the pane")
	assert.Contains(t, script, "CTXLOOM_MARKER='pane-arm'",
		"the merged environment must be passed explicitly: a tmux window otherwise inherits the shared server's env, not this run's")
	assert.NotContains(t, script, "ambient-stale",
		"a later duplicate must win, or every override BuildEnv layered on is reverted by the conversion")

	// Capture must be armed, or the pane would host the engine with nothing
	// relaying its output to the caller's terminal.
	assert.NotNil(t, r.argvFor("pipe-pane"), "the pane's output must be captured")
}

// TestInteractiveLaunch_UnnamedRunIsRefusedBeforeAnythingStarts covers the
// second breaking consequence of the single pane path. run.go warns and
// proceeds "unharped" when AssignSession fails; that run used to still get a
// working pty-backed engine, and now cannot be hosted at all because a pane is
// addressed by harp.
//
// It is pinned here so the failure states its real cause. Left unguarded it
// surfaces as tmuxhost's "a pane must name the run it belongs to", which is
// true and tells the user nothing about session naming having failed upstream.
func TestInteractiveLaunch_UnnamedRunIsRefusedBeforeAnythingStarts(t *testing.T) {
	r := &recordingRunner{}
	swapTmux(t, func() (string, error) { return "/usr/bin/tmux", nil }, r)

	code, err := RunLaunchSpec(context.Background(), agent.LaunchSpec{
		BinaryPath:  "/opt/engine/claude",
		Interactive: true,
		Harp:        "",
	}, nil, io.Discard, io.Discard, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "harp", "the error must name the cause, not just the symptom")
	assert.NotEqual(t, int32(0), code)
	assert.Zero(t, r.count(), "an unnamed run must be refused before any tmux command is issued")
}

// launcherPathFrom pulls the pane launcher's path out of a new-window command
// line. The launcher is the last word: the wrapper is handed `sh <path>` as the
// command it execs.
func launcherPathFrom(t *testing.T, joined string) string {
	t.Helper()
	fields := strings.Fields(joined)
	require.NotEmpty(t, fields)
	last := fields[len(fields)-1]
	require.True(t, strings.HasSuffix(last, ".sh"), "the pane must be launched through a launcher script, got %q", last)
	return last
}
