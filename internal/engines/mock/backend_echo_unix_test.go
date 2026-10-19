//go:build !windows

package mock

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// TestMock_InteractiveEchoReportsItsTerminalsSize: the echo reports the size
// it reads off its own terminal, as a real engine reads it — a resize applied
// to the pty between two lines shows in the second line's report.
func TestMock_InteractiveEchoReportsItsTerminalsSize(t *testing.T) {
	p, err := pty.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	up, ok := p.(pty.UnixPty)
	require.True(t, ok)
	go func() { _, _ = io.Copy(io.Discard, up.Master()) }()
	require.NoError(t, p.Resize(120, 40))
	slave := up.Slave()

	req := &agent.ExecuteRequest{
		Env:      map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		Terminal: &agent.Terminal{Stdin: slave, Stdout: slave, Stderr: slave},
	}
	out := newSyncWriter()
	done := make(chan error, 1)
	go func() {
		_, err := newTestBackend().Execute(context.Background(), req, out, out)
		done <- err
	}()
	_, err = up.Master().WriteString("first\n")
	require.NoError(t, err)
	out.waitFor(t, 5*time.Second, "mock echo: first\nmock winsize: 40x120\n")

	require.NoError(t, p.Resize(100, 30))
	_, err = up.Master().WriteString("second\nquit\n")
	require.NoError(t, err)
	out.waitFor(t, 5*time.Second, "mock echo: second\nmock winsize: 30x100\n")
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5*time.Second):
		t.Fatal("the echo did not end on quit")
	}
	assert.Equal(t, 1, strings.Count(out.String(), "mock winsize: 40x120"), "the first size is reported once, with the first line")
}
