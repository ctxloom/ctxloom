package dockerexec

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/vpio"
)

// TestBuildExecCmd_RendersTurnArgv: Start's exec CLI invocation is
// `docker exec -i -t <name> ctxloom llm turn <backend> --start <path>` with the
// TurnSpec.Env forwarded as bare `-e NAME` (values on the subprocess env, never
// the argv) — the 0600-file handoff means RunStart never rides argv/env either.
func TestBuildExecCmd_RendersTurnArgv(t *testing.T) {
	l := NewLauncher(isolation.Docker{}, "ctxloom-iso-m-abc", TurnSpec{
		Backend:   "mock",
		Label:     "fast",
		StartPath: "/home/ctxloom/.ctxloom/sessions/h/persist/runstart.json",
		Env:       map[string]string{"CTXLOOM_COORD_URL": "http://x", "CTXLOOM_SESSION_HARP": "h"},
	})
	cmd := l.buildExecCmd(context.Background())

	assert.Equal(t, "docker", cmd.Args[0], "runs the runtime binary")
	joined := strings.Join(cmd.Args, " ")
	assert.Equal(t, []string{"docker", "exec", "-i", "-t"}, cmd.Args[:4], "interactive exec head")
	assert.Contains(t, joined, "ctxloom-iso-m-abc ctxloom llm turn mock --label fast --start /home/ctxloom/.ctxloom/sessions/h/persist/runstart.json", "named container then in-container turn argv")
	assert.Contains(t, joined, "-e CTXLOOM_COORD_URL", "coord url forwarded by NAME")
	assert.Contains(t, joined, "-e CTXLOOM_SESSION_HARP", "harp forwarded by NAME")
	assert.NotContains(t, joined, "http://x", "the value never lands on the argv")
	assert.NotContains(t, joined, "runstart.json\"", "no quoting drift")

	// The values ride the subprocess env (docker forwards them from there).
	env := strings.Join(cmd.Env, "\n")
	assert.Contains(t, env, "CTXLOOM_COORD_URL=http://x")
	assert.Contains(t, env, "CTXLOOM_SESSION_HARP=h")
}

// TestBuildExecCmd_NoLabel omits --label when the TurnSpec carries none.
func TestBuildExecCmd_NoLabel(t *testing.T) {
	cmd := NewLauncher(isolation.Docker{}, "c", TurnSpec{Backend: "claude", StartPath: "/p"}).buildExecCmd(context.Background())
	assert.NotContains(t, strings.Join(cmd.Args, " "), "--label")
}

// TestSession_ResizeSetsPtyWinsize proves the vpio Resize path reaches the host
// pty master's winsize (pty.Setsize) — the first hop of the resize chain the
// daemon then forwards to the container TTY (host pty → docker CLI SIGWINCH →
// exec TTY → in-container turn's SIGWINCH). Hermetic: a real pty pair, no docker.
func TestSession_ResizeSetsPtyWinsize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A long-lived child so the pty stays open while we resize it.
	cmd := exec.CommandContext(ctx, "sleep", "5")
	sess, err := startPTYCommand(ctx, cmd, vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Signal(nil); cancel(); _, _ = sess.Wait() })

	sess.Resize(24, 80)
	rows, cols, err := pty.Getsize(sess.master)
	require.NoError(t, err)
	assert.Equal(t, 24, rows, "Resize rows reached the pty winsize")
	assert.Equal(t, 80, cols, "Resize cols reached the pty winsize")
}

// TestSession_WaitMapsEngineExitCode: a normal (non-docker-level) nonzero exit
// is the ENGINE's own code and returns as ExitStatus{Code} with a NIL error, so
// run.go's epilogue turns it into an ExitError — mirroring goplugin.Session.
func TestSession_WaitMapsEngineExitCode(t *testing.T) {
	ctx := context.Background()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "exit 7"), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	status, werr := sess.Wait()
	assert.NoError(t, werr, "an engine's own nonzero exit is not a transport error")
	assert.Equal(t, int32(7), status.Code)
}

// TestSession_WaitCleanZero maps a clean exit to code 0, nil error.
func TestSession_WaitCleanZero(t *testing.T) {
	ctx := context.Background()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "exit 0"), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	status, werr := sess.Wait()
	assert.NoError(t, werr)
	assert.Equal(t, int32(0), status.Code)
}

// TestSession_WaitDockerLevelFailureIsError: exit 125/126/127 are docker/podman
// exec's OWN failure codes (no such container, exec cannot attach, cmd not
// executable) — these surface as a LOUD error with the CLI output tail, not as
// a silent engine exit code.
func TestSession_WaitDockerLevelFailureIsError(t *testing.T) {
	ctx := context.Background()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "echo boom-tail 1>&2; exit 126"), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	_, werr := sess.Wait()
	require.Error(t, werr, "a docker-level exec failure must be a loud error")
	assert.Contains(t, werr.Error(), "126")
	assert.Contains(t, werr.Error(), "boom-tail", "the CLI output tail is surfaced")
}

// TestSession_StdinEchoesThroughPty exercises the full host-pty pump: bytes
// written to spec.Stdin reach the child through the master, and the child's
// output comes back on spec.Stdout. `cat` under a pty echoes its input, so a
// typed line round-trips — the hermetic analogue of the docker-gated
// "typed input echoes the engine's output" chain.
func TestSession_StdinEchoesThroughPty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdinR, stdinW := io.Pipe()
	var out syncBuf
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "cat"), vpio.ProcessSpec{Stdin: stdinR, Stdout: &out})
	require.NoError(t, err)

	_, _ = stdinW.Write([]byte("hello-pty\n"))
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "hello-pty") }, 3*time.Second, 20*time.Millisecond,
		"typed stdin must echo back through the host pty pump")

	_ = stdinW.Close() // EOF ends `cat`
	cancel()
	_, _ = sess.Wait()
}

// TestSession_SignalUnsupported: docker exec has no out-of-band signal channel;
// interactive interrupts ride as raw stdin bytes, so Signal reports non-support
// (never silently drops), exactly like goplugin.Session.
func TestSession_SignalUnsupported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sleep", "5"), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _, _ = sess.Wait() })
	assert.Error(t, sess.Signal(nil), "signal delivery is unsupported over docker exec")
}

// TestStart_FailsWhenBinaryMissing: Start can fail synchronously (the goplugin
// doc's anticipated docker-exec-refusing-to-attach case) — here a missing
// runtime binary — surfacing the error rather than a wedged Session.
func TestStart_FailsWhenBinaryMissing(t *testing.T) {
	l := NewLauncher(missingBinRuntime{}, "c", TurnSpec{Backend: "mock", StartPath: "/p"})
	_, err := l.Start(context.Background(), vpio.ProcessSpec{Stdout: io.Discard})
	require.Error(t, err, "a runtime whose binary is absent fails Start loudly")
}

// missingBinRuntime is a Runtime whose Binary is not on PATH, so pty.Start's
// exec fails — the Start-fails-synchronously path.
type missingBinRuntime struct{ isolation.Host }

func (missingBinRuntime) Binary() string { return "ctxloom-no-such-binary-xyz" }
func (missingBinRuntime) ExecArgs(name string, tty bool, env, command []string) []string {
	return isolation.Docker{}.ExecArgs(name, tty, env, command)
}

// syncBuf is a concurrency-safe bytes.Buffer for the output pump goroutine.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
