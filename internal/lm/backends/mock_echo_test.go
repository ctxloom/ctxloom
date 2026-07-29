package backends

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestMock_InteractiveEcho: with CTXLOOM_MOCK_ECHO_STDIN=1 the mock reflects a
// typed line and the resize it saw — the interactive engine behavior the
// docker-exec turn integration test round-trips through the pty chain. Hermetic
// (plain reader + channel, no pty/docker).
func TestMock_InteractiveEcho(t *testing.T) {
	resize := make(chan agent.WindowSize, 1)
	resize <- agent.WindowSize{Rows: 40, Cols: 120}
	req := &agent.ExecuteRequest{
		Env:    map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		Stdin:  strings.NewReader("typed-line\nquit\n"),
		Resize: resize,
	}
	var out strings.Builder
	res, err := NewMock().Execute(context.Background(), req, &out, &out)
	require.NoError(t, err)
	assert.Equal(t, int32(0), res.ExitCode)
	assert.Contains(t, out.String(), "mock echo: typed-line", "the typed line is reflected")
	assert.Contains(t, out.String(), "mock winsize: 40x120", "the resize is reflected")
}

// TestMock_EchoDisabledByDefault: without the flag the mock keeps its default
// prompt/context echo (never blocks on stdin), so existing callers are
// unaffected.
func TestMock_EchoDisabledByDefault(t *testing.T) {
	req := &agent.ExecuteRequest{
		Prompt: &agent.Fragment{Content: "hi"},
		Stdin:  strings.NewReader("should-not-be-read\n"),
	}
	var out strings.Builder
	_, err := NewMock().Execute(context.Background(), req, &out, &out)
	require.NoError(t, err)
	assert.NotContains(t, out.String(), "mock echo:", "default mode never enters the interactive echo path")
	assert.Contains(t, out.String(), "prompt=hi", "default prompt echo preserved")
}

// U057-F23: a cancelled context must interrupt executeInteractiveEcho even
// while it is blocked waiting for input — before the fix, ctx.Err() was only
// checked AFTER a ReadString call returned, so a stdin that never produces a
// line (a pty that never EOFs, or here a pipe nobody writes to) left the
// cancellation unobserved. Stdin is a pipe with no writer, so ReadString
// blocks forever; only a prompt ctx-aware return proves the fix — the old
// code would hang this test past its timeout.
func TestMock_InteractiveEcho_CtxCancelInterruptsBlockedRead(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req := &agent.ExecuteRequest{
		Env:   map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		Stdin: pr,
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	var out strings.Builder
	go func() {
		defer close(done)
		_, _ = NewMock().Execute(ctx, req, &out, &out)
	}()

	select {
	case <-done:
		// Execute returned promptly once ctx was cancelled — the fix.
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return within 2s of context cancellation; ctx.Err() is not observed while blocked in ReadString")
	}
}
