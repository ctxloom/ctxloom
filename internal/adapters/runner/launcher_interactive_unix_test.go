//go:build !windows

package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/procalive"
)

// ptyTranscript is the viewer side of an interactive launch: everything the
// engine wrote, readable while the engine is still running.
type ptyTranscript struct {
	mu sync.Mutex
	b  strings.Builder
}

func (p *ptyTranscript) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.b.Write(b)
}

func (p *ptyTranscript) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.b.String()
}

// testTerminal is a pseudo-terminal standing in for the runner's own: its
// slave is the terminal a launch is handed, its master the frontend's end,
// read into out for as long as the slave has a holder. It starts unsized, as
// the pty `ctxloom run` starts the runner on does.
type testTerminal struct {
	pty pty.UnixPty
	out *ptyTranscript
}

func newTestTerminal(t *testing.T) *testTerminal {
	t.Helper()
	p, err := pty.New()
	require.NoError(t, err)
	up, ok := p.(pty.UnixPty)
	require.True(t, ok, "a unix pty exposes its slave")
	tt := &testTerminal{pty: up, out: &ptyTranscript{}}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(tt.out, up.Master())
	}()
	// Registered first, so it runs after every launch's own cleanup has ended
	// its engine: the master read ends with EIO once the slave's last holder
	// is gone. A holder the launch left behind fails the test by name rather
	// than parking it.
	t.Cleanup(func() {
		_ = p.Close()
		select {
		case <-drained:
		case <-time.After(10 * time.Second):
			t.Errorf("the terminal never reached its end after the launch: something still holds its slave")
		}
	})
	return tt
}

// terminal is the slave as the three streams a launch hands the engine.
func (tt *testTerminal) terminal() *agent.Terminal {
	s := tt.pty.Slave()
	return &agent.Terminal{Stdin: s, Stdout: s, Stderr: s}
}

// size sets the terminal's window size, as the frontend's resize does.
func (tt *testTerminal) size(t *testing.T, rows, cols int) {
	t.Helper()
	require.NoError(t, tt.pty.Resize(cols, rows))
}

// liveLaunch is one RunLaunchSpec call running on its own goroutine.
type liveLaunch struct {
	workDir string
	done    chan struct{}
	code    int32
	err     error
}

// launchInteractive runs script through RunLaunchSpec on its own goroutine,
// as an interactive launch on term. The launch's context ends with the test,
// so an engine a failing test leaves waiting is killed rather than outliving
// it.
func launchInteractive(t *testing.T, term *agent.Terminal, script string, env ...string) *liveLaunch {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	l := &liveLaunch{workDir: t.TempDir(), done: make(chan struct{})}
	spec := agent.LaunchSpec{
		BinaryPath:  "/bin/sh",
		Args:        []string{"-c", script},
		WorkDir:     l.workDir,
		Env:         append(os.Environ(), env...),
		Interactive: true,
		Terminal:    term,
	}
	go func() {
		defer close(l.done)
		l.code, l.err = RunLaunchSpec(ctx, spec, nil, io.Discard, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		<-l.done
	})
	return l
}

// wait returns the launch's result once RunLaunchSpec has returned.
func (l *liveLaunch) wait(t *testing.T) (int32, error) {
	t.Helper()
	testsupport.Await(t, 20*time.Second, l.done, "the interactive launch never returned")
	return l.code, l.err
}

// waitFor blocks until the engine has written want. Every condition is an
// event the engine produced; the budget only bounds a failure. Once the
// launch has returned, the engine's last bytes may still be crossing the
// master, so they get a bounded look before the launch's own result is named.
func (tt *testTerminal) waitFor(t *testing.T, l *liveLaunch, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	done := l.done
	var tail <-chan time.Time
	for !strings.Contains(tt.out.String(), want) {
		select {
		case <-done:
			done, tail = nil, time.After(5*time.Second)
		case <-tail:
			t.Fatalf("the launch returned (code %d, err %v) before the engine wrote %q; it wrote:\n%q", l.code, l.err, want, tt.out.String())
		case <-deadline:
			t.Fatalf("the engine never wrote %q; it wrote:\n%q", want, tt.out.String())
		case <-time.After(time.Millisecond):
		}
	}
}

// An interactive launch hands the engine the terminal it was given, as it is:
// the engine's stdin, stdout and stderr ARE that terminal — no pty of the
// launcher's own between them — it runs where and with what the spec says,
// what is typed on the terminal reaches it, and the launch reports the
// engine's own exit status.
func TestRunLaunchSpec_InteractiveRunsTheEngineOnTheTerminalItIsGiven(t *testing.T) {
	tt := newTestTerminal(t)
	tt.size(t, 31, 97)
	name := tt.pty.Name()
	const script = `exec 3>&1 4>&2
echo "STDIN $(tty)"
echo "STDOUT $(tty 0<&3)"
echo "STDERR $(tty 0<&4)"
echo "SIZE $(stty size)"
echo "CWD $(pwd -P)"
echo "ENV $PTY_LAUNCH_PROBE"
echo READY
IFS= read -r line
echo "GOT:$line"
exit 5`
	l := launchInteractive(t, tt.terminal(), script, "PTY_LAUNCH_PROBE=from-the-spec")
	tt.waitFor(t, l, "READY")
	_, err := tt.pty.Master().Write([]byte("typed-by-the-viewer\n"))
	require.NoError(t, err)
	tt.waitFor(t, l, "GOT:typed-by-the-viewer")
	code, err := l.wait(t)

	require.NoError(t, err)
	assert.Equal(t, int32(5), code, "the launch reports the engine's own exit status")
	got := tt.out.String()
	for _, stream := range []string{"STDIN", "STDOUT", "STDERR"} {
		assert.Contains(t, got, stream+" "+name+"\r\n", "the engine's %s is the terminal the launch was given, not a pty of its own", strings.ToLower(stream))
	}
	assert.Contains(t, got, "SIZE 31 97", "the engine reads the terminal's own size")
	workDir, err := filepath.EvalSymlinks(l.workDir)
	require.NoError(t, err)
	assert.Contains(t, got, "CWD "+workDir, "the engine runs in the spec's working directory")
	assert.Contains(t, got, "ENV from-the-spec", "the engine gets the spec's environment")
	assert.Contains(t, got, "GOT:typed-by-the-viewer", "what is typed on the terminal reaches the engine")
}

// First paint: the frontend sizes the terminal only once it has taken the
// human's terminal over, which can be after the runner reaches the engine's
// launch, and an engine that queries its size before that paints its first
// screen at 0x0. So the launch holds the engine until the terminal has a
// size. The order is forced: the terminal is unsized until the launch is
// parked on it, and only then sized; the engine's FIRST query must read the
// size.
func TestRunLaunchSpec_InteractiveStartsTheEngineOnlyOnceTheTerminalHasASize(t *testing.T) {
	tt := newTestTerminal(t)
	parked := make(chan struct{})
	prev := awaitTerminalSize
	awaitTerminalSize = func(ctx context.Context, f *os.File) error {
		close(parked)
		return prev(ctx, f)
	}
	t.Cleanup(func() { awaitTerminalSize = prev })

	l := launchInteractive(t, tt.terminal(), `echo "SIZE $(stty size)"`)
	select {
	case <-parked:
	case <-l.done:
		tt.waitFor(t, l, "SIZE ")
		t.Fatalf("the engine started without waiting for the terminal's size (code %d, err %v); it read:\n%q", l.code, l.err, tt.out.String())
	case <-time.After(20 * time.Second):
		t.Fatal("the launch neither waited for the terminal's size nor returned")
	}
	tt.size(t, 31, 97)
	tt.waitFor(t, l, "SIZE ")
	code, err := l.wait(t)
	require.NoError(t, err)
	assert.Equal(t, int32(0), code)
	assert.Contains(t, tt.out.String(), "SIZE 31 97", "the engine's first size query reads the size the frontend set")
	assert.NotContains(t, tt.out.String(), "SIZE 0 0", "the engine must never start on an unsized terminal while a size is coming")
}

// A frontend with no terminal of its own never sizes the runner's, so the
// wait is bounded: the engine then starts on the terminal as it is.
func TestRunLaunchSpec_InteractiveStartsAnUnsizedEngineOnceTheWaitIsOver(t *testing.T) {
	prev := terminalSizeWait
	terminalSizeWait = 20 * time.Millisecond
	t.Cleanup(func() { terminalSizeWait = prev })
	tt := newTestTerminal(t)

	l := launchInteractive(t, tt.terminal(), `echo "SIZE $(stty size)"`)
	tt.waitFor(t, l, "SIZE 0 0")
	code, err := l.wait(t)
	require.NoError(t, err)
	assert.Equal(t, int32(0), code)
}

// A cancelled run while the launch waits for the size never starts the
// engine.
func TestRunLaunchSpec_InteractiveCancelledWhileWaitingForTheSizeStartsNothing(t *testing.T) {
	tt := newTestTerminal(t)
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	parked := make(chan struct{})
	prev := awaitTerminalSize
	awaitTerminalSize = func(ctx context.Context, f *os.File) error {
		close(parked)
		return prev(ctx, f)
	}
	t.Cleanup(func() { awaitTerminalSize = prev })

	type result struct {
		code int32
		err  error
	}
	res := make(chan result, 1)
	go func() {
		spec := agent.LaunchSpec{BinaryPath: "/bin/sh", Args: []string{"-c", `: > "$MARKER"`}, Env: append(os.Environ(), "MARKER="+marker), Interactive: true, Terminal: tt.terminal()}
		code, err := RunLaunchSpec(ctx, spec, nil, io.Discard, io.Discard)
		res <- result{code, err}
	}()
	testsupport.Await(t, 20*time.Second, parked, "the launch never waited for the terminal's size")
	cancel()
	r := testsupport.Await(t, 20*time.Second, res, "a cancelled launch never returned")
	require.ErrorIs(t, r.err, context.Canceled)
	assert.NoFileExists(t, marker, "the engine must not have started")
}

// The doubled CR the step-1 measurement found (`\r\n` arriving as `\r\r\n`)
// came from the engine's newline crossing two line disciplines: the engine's
// own pty, then the relay's writes into the runner's. On one terminal an
// engine's frame arrives byte for byte with each newline translated once.
// The frame is the shape of a scrolling claude frame: a cursor move, a line,
// a newline.
func TestRunLaunchSpec_InteractiveNewlinesCrossOneLineDiscipline(t *testing.T) {
	tt := newTestTerminal(t)
	tt.size(t, 40, 120)
	var frame strings.Builder
	for i := 29; i < 60; i++ {
		frame.WriteString("\x1b[3G" + strconv.Itoa(i) + "\n")
	}
	l := launchInteractive(t, tt.terminal(), `printf '%sFRAME-END' "$FRAME"`, "FRAME="+frame.String())
	tt.waitFor(t, l, "FRAME-END")
	code, err := l.wait(t)
	require.NoError(t, err)
	assert.Equal(t, int32(0), code)

	got, _, found := strings.Cut(tt.out.String(), "FRAME-END")
	require.True(t, found)
	assert.Equal(t, strings.ReplaceAll(frame.String(), "\n", "\r\n"), got, "each newline is translated once, by the terminal's own line discipline")
	assert.NotContains(t, got, "\r\r\n", "no second line discipline doubles the carriage return")
}

// The launch returns at the engine's exit, whatever the engine left behind
// holding the terminal: nothing in the runner reads the terminal, so nothing
// waits on its last holder. A process the engine spawned (an MCP server, a
// tool) that outlives it is the frontend's to bound (driveOwnedInteractive's
// ptyDrainGrace) and the kernel's to hang up.
func TestRunLaunchSpec_InteractiveReturnsAtTheEnginesExitWhateverHoldsTheTerminal(t *testing.T) {
	tt := newTestTerminal(t)
	tt.size(t, 24, 80)
	pidFile := filepath.Join(t.TempDir(), "holder.pid")
	l := launchInteractive(t, tt.terminal(), `sleep 30 & echo $! > "$PIDFILE"; exit 4`, "PIDFILE="+pidFile)
	code, err := l.wait(t)
	require.NoError(t, err)
	assert.Equal(t, int32(4), code)

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	assert.True(t, procalive.Alive(pid), "the holder the engine left on the terminal is still alive, and the launch returned all the same")
}

// An engine killed by a signal reports 128+signum, as the non-interactive
// branch does (TestRunLaunchSpec_NonInteractiveSignalKilledChildYields128PlusSignum).
func TestRunLaunchSpec_InteractiveSignalKilledEngineYields128PlusSignum(t *testing.T) {
	tt := newTestTerminal(t)
	tt.size(t, 24, 80)
	l := launchInteractive(t, tt.terminal(), `kill -TERM $$; sleep 5`)
	code, err := l.wait(t)
	require.NoError(t, err)
	assert.Equal(t, int32(143), code)
}

// A cancelled run ends the engine it started.
func TestRunLaunchSpec_InteractiveCancelledRunEndsTheEngine(t *testing.T) {
	tt := newTestTerminal(t)
	tt.size(t, 24, 80)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		spec := agent.LaunchSpec{BinaryPath: "/bin/sh", Args: []string{"-c", `echo UP; exec sleep 30`}, Env: os.Environ(), Interactive: true, Terminal: tt.terminal()}
		_, _ = RunLaunchSpec(ctx, spec, nil, io.Discard, io.Discard)
	}()
	tt.waitFor(t, &liveLaunch{done: done}, "UP")
	cancel()
	testsupport.Await(t, 20*time.Second, done, "a cancelled launch must end its engine and return")
}

// An interactive launch needs a terminal to hand the engine: a launch handed
// none, or streams that are not a terminal, is refused before anything runs
// rather than starting an engine that would find no terminal to draw on.
func TestRunLaunchSpec_InteractiveRefusesWhatIsNotATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	for _, tc := range []struct {
		name string
		term *agent.Terminal
	}{
		{"no terminal", nil},
		{"a pipe", &agent.Terminal{Stdin: r, Stdout: w, Stderr: w}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			spec := agent.LaunchSpec{BinaryPath: "/bin/sh", Args: []string{"-c", `: > "$MARKER"`}, Env: append(os.Environ(), "MARKER="+marker), Interactive: true, Terminal: tc.term}
			code, err := RunLaunchSpec(context.Background(), spec, nil, io.Discard, io.Discard)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "terminal")
			assert.Equal(t, int32(1), code)
			assert.NoFileExists(t, marker, "nothing may run without a terminal")
		})
	}
}
