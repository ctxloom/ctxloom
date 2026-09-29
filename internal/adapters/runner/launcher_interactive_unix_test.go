//go:build !windows

package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
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

// liveLaunch is one RunLaunchSpec call running on its own goroutine.
type liveLaunch struct {
	out  *ptyTranscript
	done chan struct{}
	code int32
	err  error
}

// launchInteractive runs spec through RunLaunchSpec on its own goroutine.
func launchInteractive(spec agent.LaunchSpec, stdin io.Reader, resize <-chan agent.WindowSize) *liveLaunch {
	l := &liveLaunch{out: &ptyTranscript{}, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		l.code, l.err = RunLaunchSpec(context.Background(), spec, stdin, l.out, io.Discard, resize)
	}()
	return l
}

// waitFor blocks until the engine has written want. Every condition is an
// event the engine produced; the budget only bounds a failure. A launch that
// returns first fails at once, naming its own result.
func (l *liveLaunch) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !strings.Contains(l.out.String(), want) {
		select {
		case <-l.done:
			if strings.Contains(l.out.String(), want) {
				return
			}
			t.Fatalf("the launch returned (code %d, err %v) before the engine wrote %q; it wrote:\n%q", l.code, l.err, want, l.out.String())
		case <-deadline:
			t.Fatalf("the engine never wrote %q; it wrote:\n%q", want, l.out.String())
		case <-time.After(time.Millisecond):
		}
	}
}

// wait returns the launch's result once RunLaunchSpec has returned.
func (l *liveLaunch) wait(t *testing.T) (int32, error) {
	t.Helper()
	select {
	case <-l.done:
		return l.code, l.err
	case <-time.After(20 * time.Second):
		t.Fatal("the interactive launch never returned")
		return 0, nil
	}
}

// An interactive launch hosts the engine on a pty the runner owns: the
// engine's stdin and stdout are a terminal already sized to the viewer's
// window before its first paint, it runs where and with what the spec says,
// the viewer's keystrokes reach it, the stdin owner's cleanup runs exactly
// once, and the launch reports the engine's own exit status. Nothing about it
// needs the run to be named, or any program besides the engine.
func TestRunLaunchSpec_InteractiveHostsTheEngineOnAPty(t *testing.T) {
	workDir := t.TempDir()
	const script = `if [ -t 0 ] && [ -t 1 ]; then echo ON-A-TTY; fi
echo "SIZE $(stty size)"
echo "CWD $(pwd -P)"
echo "ENV $PTY_LAUNCH_PROBE"
echo READY
IFS= read -r line
echo "GOT:$line"
exit 5`
	stdinR, stdinW := io.Pipe()
	var cleanups atomic.Int32
	spec := agent.LaunchSpec{
		BinaryPath:  "/bin/sh",
		Args:        []string{"-c", script},
		WorkDir:     workDir,
		Env:         append(os.Environ(), "PTY_LAUNCH_PROBE=from-the-spec"),
		Interactive: true,
		StdinCleanup: func() {
			cleanups.Add(1)
			_ = stdinR.Close()
		},
	}
	// The viewer's size is queued before the launch, as the frontend sends it
	// first: the engine must paint at it, not at the pty's default.
	resize := make(chan agent.WindowSize, 1)
	resize <- agent.WindowSize{Rows: 31, Cols: 97}

	l := launchInteractive(spec, stdinR, resize)
	l.waitFor(t, "READY")
	_, err := stdinW.Write([]byte("typed-by-the-viewer\n"))
	require.NoError(t, err)
	code, err := l.wait(t)

	require.NoError(t, err)
	assert.Equal(t, int32(5), code, "the launch reports the engine's own exit status")
	got := l.out.String()
	assert.Contains(t, got, "ON-A-TTY", "the engine's stdin and stdout are a terminal")
	assert.Contains(t, got, "SIZE 31 97", "the engine starts at the viewer's size")
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	require.NoError(t, err)
	assert.Contains(t, got, "CWD "+realWorkDir, "the engine runs in the spec's working directory")
	assert.Contains(t, got, "ENV from-the-spec", "the engine gets the spec's environment")
	assert.Contains(t, got, "GOT:typed-by-the-viewer", "the viewer's keystrokes reach the engine")
	assert.Equal(t, int32(1), cleanups.Load(), "the stdin owner's cleanup runs exactly once")
}

// What the viewer SEES of an interactive launch: the engine's screen as it
// drew it, and — after the viewer's window changes — the engine's redraw at
// the new geometry. The resize must reach the engine as a real SIGWINCH on
// its terminal, or a full-screen engine keeps painting at the old size.
func TestRunLaunchSpec_InteractiveRenderFollowsTheViewersWindow(t *testing.T) {
	stop := filepath.Join(t.TempDir(), "stop")
	// draw clears, puts the engine's geometry on the first row and a marker on
	// the LAST row the engine believes it has. The trap is installed before
	// the first draw, so the test's wait for that draw orders the resize after
	// it.
	const script = `draw() { set -- $(stty size); printf '\033[H\033[2JENGINE %sx%s\033[%s;1HBOTTOM' "$1" "$2" "$1"; }
trap draw WINCH
draw
while [ ! -e "$STOP" ]; do sleep 0.02; done`
	spec := agent.LaunchSpec{
		BinaryPath:  "/bin/sh",
		Args:        []string{"-c", script},
		WorkDir:     t.TempDir(),
		Env:         append(os.Environ(), "STOP="+stop),
		Interactive: true,
	}
	resize := make(chan agent.WindowSize, 2)
	resize <- agent.WindowSize{Rows: 12, Cols: 40}

	l := launchInteractive(spec, nil, resize)
	l.waitFor(t, "ENGINE 12x40")
	before := vtemu.New(12, 40)
	before.Feed([]byte(l.out.String()))
	require.Empty(t, before.Unhandled(), "every byte the engine drew must be understood")
	assert.Equal(t, "ENGINE 12x40", before.Row(0), "the engine's screen:\n%s", before)
	assert.Equal(t, "BOTTOM", before.Row(11), "the engine drew to the bottom of the window it was given:\n%s", before)

	resize <- agent.WindowSize{Rows: 20, Cols: 60}
	l.waitFor(t, "ENGINE 20x60")
	after := vtemu.New(20, 60)
	after.Feed([]byte(l.out.String()))
	require.Empty(t, after.Unhandled(), "every byte the engine drew must be understood")
	assert.Equal(t, "ENGINE 20x60", after.Row(0), "the engine redrew at the new size:\n%s", after)
	assert.Equal(t, "BOTTOM", after.Row(19), "the redraw reaches the new bottom row:\n%s", after)
	for r := 1; r < 19; r++ {
		assert.Empty(t, after.Row(r), "nothing of the old frame survives the redraw:\n%s", after)
	}

	require.NoError(t, os.WriteFile(stop, nil, 0o600))
	code, err := l.wait(t)
	require.NoError(t, err)
	assert.Equal(t, int32(0), code)
}
