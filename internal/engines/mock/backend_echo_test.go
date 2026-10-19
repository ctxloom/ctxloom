package mock

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// pipeTerminal is a terminal stand-in over pipes for an engine running in
// this process: Stdin reads typed, then EOF; Stdout and Stderr are a pipe
// nobody reads, whose size cannot be asked.
func pipeTerminal(t *testing.T, typed string) *agent.Terminal {
	t.Helper()
	inR, inW, err := os.Pipe()
	require.NoError(t, err)
	_, err = inW.WriteString(typed)
	require.NoError(t, err)
	require.NoError(t, inW.Close())
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = inR.Close(); _ = outR.Close(); _ = outW.Close() })
	return &agent.Terminal{Stdin: inR, Stdout: outW, Stderr: outW}
}

// TestMock_InteractiveEcho: with CTXLOOM_MOCK_ECHO_STDIN=1 the mock reflects a
// line typed on its terminal — the interactive engine behavior the docker
// interactive integration test round-trips through the pty chain. A terminal
// whose size cannot be asked reports none.
func TestMock_InteractiveEcho(t *testing.T) {
	req := &agent.ExecuteRequest{
		Env:      map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		Terminal: pipeTerminal(t, "typed-line\nquit\n"),
	}
	var out strings.Builder
	res, err := newTestBackend().Execute(context.Background(), req, &out, &out)
	require.NoError(t, err)
	assert.Equal(t, int32(0), res.ExitCode)
	assert.Contains(t, out.String(), "mock echo: typed-line", "the typed line is reflected")
	assert.NotContains(t, out.String(), "mock winsize:", "a pipe has no size to report")
}

// TestMock_EchoDisabledByDefault: without the flag the mock keeps its default
// prompt/context echo (never blocks on its terminal), so existing callers are
// unaffected.
func TestMock_EchoDisabledByDefault(t *testing.T) {
	req := &agent.ExecuteRequest{
		Prompt:   &agent.Fragment{Content: "hi"},
		Terminal: pipeTerminal(t, "should-not-be-read\n"),
	}
	var out strings.Builder
	_, err := newTestBackend().Execute(context.Background(), req, &out, &out)
	require.NoError(t, err)
	assert.NotContains(t, out.String(), "mock echo:", "default mode never enters the interactive echo path")
	assert.Contains(t, out.String(), "prompt=hi", "default prompt echo preserved")
}

// A cancelled context must interrupt executeInteractiveEcho even
// while it is blocked waiting for input — before the fix, ctx.Err() was only
// checked AFTER a ReadString call returned, so a stdin that never produces a
// line (a pty that never EOFs, or here a pipe nobody writes to) left the
// cancellation unobserved. Stdin is a pipe nobody writes, so ReadString
// blocks forever; only a prompt ctx-aware return proves the fix — the old
// code would hang this test past its timeout.
func TestMock_InteractiveEcho_CtxCancelInterruptsBlockedRead(t *testing.T) {
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	defer pw.Close()
	defer pr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req := &agent.ExecuteRequest{
		Env:      map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		Terminal: &agent.Terminal{Stdin: pr, Stdout: pw, Stderr: pw},
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	var out strings.Builder
	go func() {
		defer close(done)
		_, _ = newTestBackend().Execute(ctx, req, &out, &out)
	}()

	select {
	case <-done:
		// Execute returned promptly once ctx was cancelled — the fix.
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return within 2s of context cancellation; ctx.Err() is not observed while blocked in ReadString")
	}
}
