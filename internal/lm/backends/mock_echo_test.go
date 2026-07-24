package backends

import (
	"context"
	"strings"
	"testing"

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
		Stdin:  strings.NewReader("typed-line\n"),
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
