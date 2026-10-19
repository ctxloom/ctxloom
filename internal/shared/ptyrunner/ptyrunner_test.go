//go:build !windows

package ptyrunner

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runOnPty starts cmd through Command on a fresh pty and returns everything
// the child wrote and its exit, once it has exited and its output is drained.
func runOnPty(t *testing.T, cmd *exec.Cmd) (string, error) {
	t.Helper()
	ptty, err := pty.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ptty.Close() })
	c := Command(context.Background(), ptty, cmd)
	require.NoError(t, c.Start())
	// The child holds its own copies of the slave; this process's must go so
	// the master reads EIO once the child's last byte is drained.
	up, ok := ptty.(pty.UnixPty)
	require.True(t, ok)
	_ = up.Slave().Close()
	var out strings.Builder
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(&out, ptty)
	}()
	select {
	case <-drained:
	case <-time.After(20 * time.Second):
		t.Fatal("the child's output never ended")
	}
	return out.String(), c.Wait()
}

// TestCommand_HandBuiltCmdWithoutArgsRuns pins the nil-Args crash: os/exec
// defaults an empty Args to a one-element slice containing Path, but it does
// that inside exec.Cmd.Start, which Command never calls — it re-derives the
// argv for go-pty's own Cmd instead. So a hand-built &exec.Cmd{Path: ...}
// (legal, and the shape any caller assembling a spec rather than calling
// exec.Command produces) still carries a nil Args when it arrives here, and
// slicing it at [1:] panics.
func TestCommand_HandBuiltCmdWithoutArgsRuns(t *testing.T) {
	truePath, err := exec.LookPath("true")
	require.NoError(t, err)

	// Args deliberately left nil — that is the whole fixture.
	cmd := &exec.Cmd{Path: truePath}
	require.Nil(t, cmd.Args, "the fixture is only hostile while Args is nil")

	var runErr error
	require.NotPanics(t, func() { _, runErr = runOnPty(t, cmd) }, "a *exec.Cmd with no Args must not panic the start")
	require.NoError(t, runErr)
}

// TestCommand_Argv0ComesFromPath characterizes a limitation of the underlying
// pty library rather than a choice made here: go-pty's Cmd.start rebuilds the
// child's argv as exec.Command(c.Path, c.Args[1:]...), so argv[0] is ALWAYS
// the resolved Path and a caller's own Args[0] can never survive.
// exec.Command itself sets Args[0] to the name as written ("sh"), so the two
// already differ for every ordinary caller. Pinned so that a future change of
// pty library — the only way to honour a custom argv[0] — is a deliberate,
// visible decision rather than a silent behaviour change.
func TestCommand_Argv0ComesFromPath(t *testing.T) {
	cmd := exec.Command("sh", "-c", `printf 'argv0=%s\n' "$0"`)
	require.Equal(t, "sh", cmd.Args[0], "exec.Command sets argv[0] to the name as written")
	require.NotEqual(t, cmd.Args[0], cmd.Path, "the fixture needs Path and Args[0] to differ")

	out, err := runOnPty(t, cmd)
	require.NoError(t, err)
	assert.Contains(t, out, "argv0="+cmd.Path,
		"go-pty derives argv[0] from Path; the caller's Args[0] is not reachable from here")
}
