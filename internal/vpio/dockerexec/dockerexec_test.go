package dockerexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
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

	// The turn process is forced to TERM=dumb (forwarded bare) so ctxloom's
	// init-time terminal query never fires and eats the interactive stdin.
	assert.Contains(t, joined, "-e TERM", "TERM is forwarded to the turn process")
	assert.Contains(t, env, "TERM=dumb", "the turn process runs under TERM=dumb")
}

// TestStartPTYCommand_NilStdoutErrors pins that a nil spec.Stdout used
// to silently substitute io.Discard — the pump ran, Wait still returned
// ExitStatus{Code: 0}, nil, and the entire interactive session's output
// vanished with no error, no warning, no log at all: exit 0, success, zero
// bytes delivered. It must now refuse instead of guessing.
func TestStartPTYCommand_NilStdoutErrors(t *testing.T) {
	ctx := context.Background()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "true"), vpio.ProcessSpec{})
	require.Error(t, err, "a nil Stdout must be refused, not silently discarded")
	assert.Nil(t, sess)
	assert.Contains(t, err.Error(), "Stdout is nil")
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
	t.Cleanup(func() { cancel(); _, _ = sess.Wait() })

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

// TestSession_RingIsSharedStderrtailImplementation pins that Session's
// output-tail ring must be the ONE shared stderrtail.Ring implementation
// (internal/shared/stderrtail), not the byte-for-byte private duplicate this
// package grew independently — one day AFTER stderrtail was written
// specifically to be "the one implementation" of exactly this pattern
// (bounded, concurrency-safe last-N-bytes tail for a dying child's
// diagnostics) — and never absorbed onto it.
func TestSession_RingIsSharedStderrtailImplementation(t *testing.T) {
	ctx := context.Background()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "true"), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	assert.IsType(t, &stderrtail.Ring{}, sess.ring, "Session must use stderrtail.Ring, not a private duplicate")
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

// TestSession_OutputTailIsBoundedAtTheSharedBudget pins the BEHAVIOUR the
// shared-ring consolidation was for, which its own type assertion above cannot
// observe: `sess.ring` is statically *stderrtail.Ring, so IsType is a tautology
// that no regression can make fail. The property that actually distinguishes
// the shared ring from a re-grown private one is the budget it is built with —
// stderrtail.DefaultBytes, the single standard tail budget — and that a tail is
// the LAST bytes of the stream, not the first. A private ring with its own
// constant would fail here.
func TestSession_OutputTailIsBoundedAtTheSharedBudget(t *testing.T) {
	ctx := context.Background()
	// One byte-run longer than the budget, no newlines (nothing for the pty's
	// output processing to rewrite), ending in a docker-level code so Wait
	// renders the tail into its error.
	const over = stderrtail.DefaultBytes + 512
	script := "head -c " + strconv.Itoa(over) + " /dev/zero | tr '\\0' 'x'; printf END; exit 126"
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", script), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	_, werr := sess.Wait()
	require.Error(t, werr)

	tail := sess.ring.Tail()
	assert.Len(t, tail, stderrtail.DefaultBytes, "the tail is bounded at the ONE shared budget, not a per-package constant")
	assert.True(t, strings.HasSuffix(tail, "END"), "a tail keeps the LAST bytes of the stream")
}

// TestSession_WaitClassifiesEveryDockerLevelCode characterizes the whole
// classification arm-by-arm before the bare 125/126/127 literals are named:
// exactly those three codes are the RUNTIME's own failures (loud error, tail
// attached); every neighbouring code is the ENGINE's own exit status and comes
// back as ExitStatus{Code} with a nil error for run.go's epilogue to turn into
// an ExitError. The boundaries are the point — 124 and 128 must stay engine
// codes.
func TestSession_WaitClassifiesEveryDockerLevelCode(t *testing.T) {
	for _, tc := range []struct {
		code        int
		dockerLevel bool
	}{
		{0, false},
		{1, false},
		{124, false},
		{125, true},
		{126, true},
		{127, true},
		{128, false},
	} {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			ctx := context.Background()
			sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "exit "+strconv.Itoa(tc.code)), vpio.ProcessSpec{Stdout: io.Discard})
			require.NoError(t, err)
			status, werr := sess.Wait()
			if tc.dockerLevel {
				require.Error(t, werr, "%d is the runtime's own failure, not the engine's exit status", tc.code)
				assert.Contains(t, werr.Error(), "the runtime, not the engine")
				return
			}
			require.NoError(t, werr, "%d is the engine's own exit status, not a transport error", tc.code)
			assert.Equal(t, int32(tc.code), status.Code)
		})
	}
}

// TestDockerLevelError_ParityAcrossBothRenderings is the parity test across the
// two copies of the "attach the captured output tail to a docker-level failure"
// helper (dockerLevelError and dockerLevelErrorWrap) taken BEFORE collapsing
// them. They agree on the branch (tail present / tail empty) but NOT on how the
// tail is rendered: one appends `: <tail>`, the other ` (output tail: <tail>)`.
// The same captured bytes are therefore labelled two different ways depending
// on which docker-level failure a user hits, and only one of the two labels is
// greppable. That divergence is the defect the duplication was hiding.
func TestDockerLevelError_ParityAcrossBothRenderings(t *testing.T) {
	withTail := &Session{ring: stderrtail.New(stderrtail.DefaultBytes)}
	_, _ = withTail.ring.Write([]byte("boom-tail"))
	empty := &Session{ring: stderrtail.New(stderrtail.DefaultBytes)}

	// Branch parity: both include the tail when there is one and omit any tail
	// clause when there is not.
	assert.Contains(t, withTail.dockerLevelError(exitCannotInvoke).Error(), "boom-tail")
	assert.Contains(t, withTail.dockerLevelErrorWrap(errors.New("signal: killed")).Error(), "boom-tail")
	assert.NotContains(t, empty.dockerLevelError(exitCannotInvoke).Error(), "tail")
	assert.NotContains(t, empty.dockerLevelErrorWrap(errors.New("signal: killed")).Error(), "tail")

	// Rendering parity: the same captured bytes must be labelled the same way
	// whichever docker-level failure produced them.
	suffixOf := func(err error) string {
		s := err.Error()
		return s[strings.Index(s, "boom-tail")-len(" (output tail: "):]
	}
	assert.Equal(t,
		suffixOf(withTail.dockerLevelErrorWrap(errors.New("signal: killed"))),
		suffixOf(withTail.dockerLevelError(exitCannotInvoke)),
		"one tail, one rendering — a reader (or a grep) must not have to know which failure fired")
}

// TestStartPTYCommand_CancellationIsGraceful pins that the turn subprocess
// is built with exec.CommandContext and neither cmd.Cancel nor cmd.WaitDelay
// was set, so os/exec's default cancellation is Process.Kill() — SIGKILL, zero
// grace. The subprocess here is the `docker exec` CLI attached to a live
// in-container turn; SIGKILLing it drops the attachment without the CLI ever
// telling the daemon, so nothing in the container is asked to stop and the
// session's own teardown never runs. Cancellation must ASK first and kill only
// as a backstop.
//
// The child traps SIGTERM and records that it arrived; under SIGKILL nothing is
// recorded, because SIGKILL cannot be trapped. Bounded: the child exits on the
// signal and Wait returns immediately either way.
func TestStartPTYCommand_CancellationIsGraceful(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "sigterm-arrived")
	script := "trap 'printf x > " + marker + "; exit 0' TERM; printf ready; while : ; do sleep 0.05; done"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuf
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", script), vpio.ProcessSpec{Stdout: &out})
	require.NoError(t, err)

	// The trap must be installed before we cancel, or the test measures the
	// race rather than the signal (template §11k: prove the fixture is armed).
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "ready") },
		5*time.Second, 20*time.Millisecond, "child never reached its trap")

	cancel()
	_, _ = sess.Wait()
	require.FileExists(t, marker, "the subprocess was killed with zero grace instead of being asked to stop")
}

// TestSession_WaitClosesThePtyMaster pins that the host pty master was
// closed ONLY on Wait's drain-timeout backstop arm. On the normal exit path —
// every healthy turn — the fd stayed open for the life of the process, so a
// frontend driving several container turns under one long-lived context leaked
// one master fd per turn. Wait returning means the session is over; its fd goes
// with it. A second Close reporting os.ErrClosed is how the fd's state is
// observable from outside.
func TestSession_WaitClosesThePtyMaster(t *testing.T) {
	ctx := context.Background()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "exit 0"), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	_, werr := sess.Wait()
	require.NoError(t, werr)

	require.ErrorIs(t, sess.master.Close(), os.ErrClosed, "the pty master must be closed once Wait returns")
}

// TestStartPTYCommand_StdinPumpRetiresWithTheSession pins that the
// stdin-copy goroutine was fire-and-forget — nothing cancelled it and nothing
// could observe it. Its danger was not the goroutine but its WRITE END: it held
// the pty master, which the session never closed, so a keystroke
// arriving after the turn ended was copied into a descriptor whose owner had
// moved on. Once the master is closed with the session, that write can only
// fail, and the pump retires.
//
// This is a test-seam row (template §4 class 4): inDone did not exist before,
// so the honest pre-fix test does not compile. It is demonstrated red with the
// SEAM PRESENT and only Wait's unconditional closeMaster reverted — see the
// commit body.
func TestStartPTYCommand_StdinPumpRetiresWithTheSession(t *testing.T) {
	ctx := context.Background()
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })

	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "exit 0"), vpio.ProcessSpec{Stdin: stdinR, Stdout: io.Discard})
	require.NoError(t, err)
	_, werr := sess.Wait()
	require.NoError(t, werr)

	// One keystroke after the session is over. It must go nowhere.
	go func() { _, _ = stdinW.Write([]byte("k")) }()

	select {
	case <-sess.inDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the stdin pump is still live after the session ended — it is still holding the master's write end")
	}
}

// TestStartPTYCommand_NilStdinRetiresImmediately: a non-interactive turn has no
// pump at all, and inDone must not be a channel nobody ever closes.
func TestStartPTYCommand_NilStdinRetiresImmediately(t *testing.T) {
	ctx := context.Background()
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "exit 0"), vpio.ProcessSpec{Stdout: io.Discard})
	require.NoError(t, err)
	select {
	case <-sess.inDone:
	case <-time.After(2 * time.Second):
		t.Fatal("inDone must already be closed when there is no stdin to pump")
	}
}

// TestSession_ResizeReportsAFailedIoctl pins that Resize discarded
// pty.Setsize's error outright, so a resize that never reached the container
// produced no error, no warning and no log — the container's TTY silently kept
// the old geometry for the whole turn while the agent redrew into the wrong
// box. vpio.Session.Resize returns nothing (that seam is not this package's to
// change), so the frontend's diagnostic stream is where it has to be said.
func TestSession_ResizeReportsAFailedIoctl(t *testing.T) {
	// A regular file is not a tty, so the Setsize ioctl fails — a live session
	// whose resize cannot land, which is exactly the case that was silent.
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	var diag syncBuf
	s := &Session{master: f, stderr: &diag}

	s.Resize(24, 80)
	assert.Contains(t, diag.String(), "did not reach the container", "a resize that never landed must be audible")

	// SIGWINCH fires per drag frame; a failing ioctl must not warn per pixel.
	before := diag.String()
	s.Resize(30, 100)
	s.Resize(40, 120)
	assert.Equal(t, before, diag.String(), "one warning per session, not one per resize")
}

// TestSession_ResizeAfterTheSessionEndsIsSilent: once the session is over there
// is nothing to resize, and the seam documents Resize as drop-rather-than-stall
// (goplugin.Session does the same). A late SIGWINCH must not turn into a
// warning about a session the user already finished.
func TestSession_ResizeAfterTheSessionEndsIsSilent(t *testing.T) {
	ctx := context.Background()
	var diag syncBuf
	sess, err := startPTYCommand(ctx, exec.CommandContext(ctx, "sh", "-c", "exit 0"), vpio.ProcessSpec{Stdout: io.Discard, Stderr: &diag})
	require.NoError(t, err)
	_, werr := sess.Wait()
	require.NoError(t, werr)

	sess.Resize(24, 80)
	assert.Empty(t, diag.String(), "a resize after the session ended is dropped, not reported")
}

// TestStart_RefusesAnIncompleteTurn pins that nothing validated the
// Launcher's own inputs, so an empty Backend, StartPath or container name still
// rendered a WELL-FORMED command — `docker exec -i -t "" ctxloom llm turn
// --start ""` — and the first thing that noticed was the runtime, or the
// in-container ctxloom, with a message about argv rather than about the field
// the caller left blank. The transport knows exactly which field is missing;
// it should say so before it spawns anything.
func TestStart_RefusesAnIncompleteTurn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		container string
		turn      TurnSpec
		wants     string
	}{
		{"no container", "", TurnSpec{Backend: "mock", StartPath: "/p"}, "container"},
		{"no backend", "c", TurnSpec{StartPath: "/p"}, "backend"},
		{"no start path", "c", TurnSpec{Backend: "mock"}, "start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// missingBinRuntime so a pre-fix run cannot spawn anything real.
			_, err := NewLauncher(missingBinRuntime{}, tc.container, tc.turn).
				Start(context.Background(), vpio.ProcessSpec{Stdout: io.Discard})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wants, "the error names the field the caller left blank")
		})
	}
}

// TestStartPTYCommand_SessionStderrMergesIntoStdout pins what this transport
// can and cannot separate, which is what vpio.ProcessSpec.Stderr's doc comment
// now says: the turn runs on ONE pty, and a pty has a single stream, so the
// session's own stderr is interleaved into spec.Stdout by the kernel and never
// reaches spec.Stderr. spec.Stderr carries this transport's own diagnostics
// (Session.warn) and nothing else.
//
// This is a property of the pty, not a shortcut: if a future change ever does
// route the session's stderr separately, this test fails and the seam's
// documented contract has to be revisited rather than quietly drifting.
func TestStartPTYCommand_SessionStderrMergesIntoStdout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out, errs syncBuf
	sess, err := startPTYCommand(ctx,
		exec.CommandContext(ctx, "sh", "-c", "echo on-stdout; echo on-stderr 1>&2"),
		vpio.ProcessSpec{Stdout: &out, Stderr: &errs})
	require.NoError(t, err)
	_, _ = sess.Wait()

	assert.Contains(t, out.String(), "on-stdout")
	assert.Contains(t, out.String(), "on-stderr",
		"a pty carries one stream: the session's stderr must arrive interleaved on Stdout")
	assert.NotContains(t, errs.String(), "on-stderr",
		"spec.Stderr is the transport's own diagnostic channel, not the session's stderr")
}

// TestSession_WaitIsIdempotent pins the multiplicity half of vpio.Session.Wait's
// contract on this transport, the half the seam now states explicitly: the
// terminal result is delivered once and cached, so a caller may write the
// natural `defer session.Wait()` alongside an explicit one and get the same
// answer twice instead of parking on a channel nothing will write again. The
// sibling goplugin transport promises the same thing (its own
// TestSession_WaitIsIdempotent).
//
// The second Wait is bounded rather than merely called: a Wait that PARKS is a
// hang wearing a failure's clothes, and an unbounded one would burn the whole
// test timeout instead of failing.
func TestSession_WaitIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out syncBuf
	sess, err := startPTYCommand(ctx,
		exec.CommandContext(ctx, "sh", "-c", "exit 7"),
		vpio.ProcessSpec{Stdout: &out})
	require.NoError(t, err)

	first, firstErr := sess.Wait()
	require.NoError(t, firstErr)
	require.Equal(t, int32(7), first.Code)

	done := make(chan struct{})
	var second vpio.ExitStatus
	var secondErr error
	go func() {
		defer close(done)
		second, secondErr = sess.Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a second Wait parked; the seam documents Wait as idempotent")
	}

	assert.Equal(t, first, second, "a later Wait must return the same exit status")
	assert.Equal(t, firstErr, secondErr, "a later Wait must return the same error")
}

// TestSession_CtxCancellationAloneDoesNotReleaseThePtyMaster pins the LIMIT of
// the lifecycle contract vpio.Session now states: cancelling the ctx passed to
// Start ASKS the turn to end, but Wait is the release point, and until it is
// called this transport is still holding the host pty master.
//
// This is a characterization of a deliberate boundary, not an endorsement of
// it. If a future change ever does release on cancellation — or the seam grows
// the explicit Close/Release this shape argues for — this test fails, and the
// documented contract has to be revisited rather than silently drifting out of
// step with the code.
func TestSession_CtxCancellationAloneDoesNotReleaseThePtyMaster(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out syncBuf
	sess, err := startPTYCommand(ctx,
		exec.CommandContext(ctx, "sh", "-c", "sleep 30"),
		vpio.ProcessSpec{Stdout: &out})
	require.NoError(t, err)

	masterClosed := func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.masterClosed
	}
	require.False(t, masterClosed(), "a live session holds its master")

	cancel()
	require.Never(t, masterClosed, 500*time.Millisecond, 50*time.Millisecond,
		"ctx cancellation must not be mistaken for the release point")

	_, _ = sess.Wait()
	require.True(t, masterClosed(), "Wait is the release point")
}
