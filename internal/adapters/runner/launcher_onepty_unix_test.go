//go:build !windows

package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
)

// These tests put the launch where production puts it: in a runner PROCESS
// started on a pty whose master the test holds, as `ctxloom run` starts
// `ctxloom runner` (hostpty.Start). The runner leads its session on that pty,
// so the pty is its controlling terminal and the engine's — which is what
// carries a resize to the engine as a SIGWINCH, and what an in-process launch
// cannot reproduce.

// launchHelperEnv marks this test binary's re-exec as the runner.
const launchHelperEnv = "CTXLOOM_RUNNER_LAUNCH_HELPER"

// TestHelperRunnerOnAPty is not a real test: it is the re-exec target
// startRunnerOnAPty starts (the standard library's TestHelperProcess idiom).
// It plays the runner: an interactive launch of `sh -c <script>` on its own
// stdio, exiting with the engine's status. Guarded by launchHelperEnv, so
// `go test` running it as an ordinary test is a no-op.
func TestHelperRunnerOnAPty(t *testing.T) {
	if os.Getenv(launchHelperEnv) != "1" {
		return
	}
	script := os.Args[len(os.Args)-1]
	code, err := RunLaunchSpec(context.Background(), agent.LaunchSpec{
		BinaryPath:  "/bin/sh",
		Args:        []string{"-c", script},
		Env:         os.Environ(),
		Interactive: true,
		Terminal:    &agent.Terminal{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr},
	}, nil, io.Discard, io.Discard)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runner helper: %v\n", err)
		os.Exit(97)
	}
	os.Exit(int(code))
}

// frontendPty is the test's side of the runner's pty: the hostpty session
// and everything read off its master.
type frontendPty struct {
	s       hostpty.Session
	out     *ptyTranscript
	drained chan struct{}
}

// startRunnerOnAPty starts the runner helper on a fresh pty, the master held
// here and drained for the life of the test.
func startRunnerOnAPty(t *testing.T, script string, env ...string) *frontendPty {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRunnerOnAPty$", "--", script)
	cmd.Env = append(append(os.Environ(), launchHelperEnv+"=1"), env...)
	s, err := hostpty.Start(context.Background(), cmd)
	require.NoError(t, err)
	t.Cleanup(s.Kill)
	f := &frontendPty{s: s, out: &ptyTranscript{}, drained: make(chan struct{})}
	go func() {
		defer close(f.drained)
		_, _ = io.Copy(f.out, s.Master())
	}()
	return f
}

// name is the pty's slave: the terminal the runner was started on.
func (f *frontendPty) name(t *testing.T) string {
	t.Helper()
	named, ok := f.s.Master().(interface{ Name() string })
	require.True(t, ok, "the pty names its slave")
	return named.Name()
}

// waitFor blocks until the master carried want; the budget bounds a failure.
func (f *frontendPty) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !strings.Contains(f.out.String(), want) {
		select {
		case <-f.drained:
			if strings.Contains(f.out.String(), want) {
				return
			}
			t.Fatalf("the pty ended before it carried %q; it carried:\n%q", want, f.out.String())
		case <-deadline:
			t.Fatalf("the pty never carried %q; it carried:\n%q", want, f.out.String())
		case <-time.After(time.Millisecond):
		}
	}
}

// exit waits for the runner's exit and its last bytes.
func (f *frontendPty) exit(t *testing.T) int {
	t.Helper()
	select {
	case <-f.drained:
	case <-time.After(20 * time.Second):
		t.Fatalf("the runner's pty never ended; it carried:\n%q", f.out.String())
	}
	code, err := f.s.Wait()
	require.NoError(t, err)
	return code
}

// The engine runs on exactly one pty: the one the frontend holds the master
// of. Asked from the engine's side, its stdin, stdout and stderr are that
// pty — not a second one the runner allocated and relayed — keystrokes the
// frontend writes reach it, and its exit status is the runner's.
func TestRunLaunchSpec_TheEngineIsOnThePtyTheFrontendHolds(t *testing.T) {
	const script = `exec 3>&1 4>&2
echo "STDIN $(tty)"
echo "STDOUT $(tty 0<&3)"
echo "STDERR $(tty 0<&4)"
echo READY
IFS= read -r line
echo "GOT:$line"
exit 5`
	f := startRunnerOnAPty(t, script)
	require.NoError(t, f.s.Resize(24, 80), "the frontend's first size")
	f.waitFor(t, "READY")
	_, err := f.s.Master().Write([]byte("typed-by-the-frontend\n"))
	require.NoError(t, err)
	f.waitFor(t, "GOT:typed-by-the-frontend")
	assert.Equal(t, 5, f.exit(t), "the runner exits with the engine's status")

	name := f.name(t)
	got := f.out.String()
	for _, stream := range []string{"STDIN", "STDOUT", "STDERR"} {
		assert.Contains(t, got, stream+" "+name+"\r\n", "the engine's %s is the frontend's pty", strings.ToLower(stream))
	}
}

// A resize is one hop: the size the frontend sets on its pty is the size the
// engine reads, delivered as a SIGWINCH on the engine's own terminal, with
// nothing in between to relay it. What the frontend sees is the engine's
// redraw at the new geometry.
func TestRunLaunchSpec_AResizeOfTheFrontendsPtyReachesTheEngineInOneHop(t *testing.T) {
	stop := filepath.Join(t.TempDir(), "stop")
	// draw clears, puts the engine's geometry on the first row and a marker on
	// the LAST row the engine believes it has. The trap is installed before
	// the first draw, so the test's wait for that draw orders the resize after
	// it.
	const script = `draw() { set -- $(stty size); printf '\033[H\033[2JENGINE %sx%s\033[%s;1HBOTTOM' "$1" "$2" "$1"; }
trap draw WINCH
draw
while [ ! -e "$STOP" ]; do sleep 0.02; done`
	f := startRunnerOnAPty(t, script, "STOP="+stop)
	require.NoError(t, f.s.Resize(12, 40), "the frontend's first size")
	f.waitFor(t, "ENGINE 12x40")
	before := vtemu.New(12, 40)
	before.Feed([]byte(f.out.String()))
	require.Empty(t, before.Unhandled(), "every byte the engine drew must be understood")
	assert.Equal(t, "ENGINE 12x40", before.Row(0), "the engine's screen:\n%s", before)
	assert.Equal(t, "BOTTOM", before.Row(11), "the engine drew to the bottom of the window it was given:\n%s", before)

	require.NoError(t, f.s.Resize(20, 60))
	f.waitFor(t, "ENGINE 20x60")
	after := vtemu.New(20, 60)
	after.Feed([]byte(f.out.String()))
	require.Empty(t, after.Unhandled(), "every byte the engine drew must be understood")
	assert.Equal(t, "ENGINE 20x60", after.Row(0), "the engine redrew at the new size:\n%s", after)
	assert.Equal(t, "BOTTOM", after.Row(19), "the redraw reaches the new bottom row:\n%s", after)
	for r := 1; r < 19; r++ {
		assert.Empty(t, after.Row(r), "nothing of the old frame survives the redraw:\n%s", after)
	}

	require.NoError(t, os.WriteFile(stop, nil, 0o600))
	assert.Equal(t, 0, f.exit(t))
}
