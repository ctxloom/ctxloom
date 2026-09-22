package agent

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- ExecuteEnv seam --------------------------------------------------------

// TestExecuteEnv_MergesExtraEnv proves the per-backend env contributor
// (SetExecuteEnv) is merged on top of the request env, and wins on a key
// clash.
func TestExecuteEnv_MergesExtraEnv(t *testing.T) {
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.SetExecuteEnv(func(req *ExecuteRequest) map[string]string {
		return map[string]string{"CODEX_HOME": filepath.Join(req.WorkDir, ".codex")}
	})

	env := b.ExecuteEnv(&ExecuteRequest{WorkDir: "/w", Env: map[string]string{"KEEP": "1"}})
	assert.Equal(t, "1", env["KEEP"], "request env is preserved")
	assert.Equal(t, filepath.Join("/w", ".codex"), env["CODEX_HOME"], "the contributor's env is merged in")
}

// ---- ExecuteCLI: stdin cleanup relay ----------------------------------------

// newSpecCapturingBackend returns a LaunchBackend whose launcher records the
// LaunchSpec it was handed instead of execing anything, so a test can read what
// ExecuteCLI actually assembled for the runtime.
func newSpecCapturingBackend() (*LaunchBackend, *LaunchSpec) {
	var captured LaunchSpec
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.BinaryPath = "/bin/true"
	b.SetLauncher(func(_ context.Context, spec LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan WindowSize) (int32, error) {
		captured = spec
		return 0, nil
	})
	return b, &captured
}

// TestExecuteCLI_InteractiveRelaysStdinCleanup pins the middle of the stdin
// ownership chain, which nothing else reaches. The cleanup travels
// GRPCServer.Run (which makes the io.Pipe and alone may close it) →
// ExecuteRequest.StdinCleanup → ExecuteCLI → RunInteractive → LaunchSpec →
// ptyrunner. The grpc suite pins the top of that chain and the ptyrunner suite
// pins the bottom, but ExecuteCLI is the shared exec tail EVERY exec-style
// backend funnels through, and it had no test at all — so replacing
// req.StdinCleanup with a hardcoded nil here passed the whole suite while
// silently restoring the wedge one layer below where it was fixed.
//
// Asserting non-nil alone would not be enough: it survives a relay that
// fabricates some other closure. The assertion is that the caller's OWN
// cleanup is what reaches the spec, observed by its effect.
func TestExecuteCLI_InteractiveRelaysStdinCleanup(t *testing.T) {
	b, captured := newSpecCapturingBackend()

	released := false
	_, err := b.ExecuteCLI(context.Background(), &ExecuteRequest{
		Mode:         ModeInteractive,
		Stdin:        bytes.NewReader(nil),
		StdinCleanup: func() { released = true },
	}, nil, nil, nil, io.Discard, io.Discard)
	require.NoError(t, err)

	require.NotNil(t, captured.StdinCleanup,
		"the caller's stdin cleanup must reach the launch spec: without it nothing retires the wire stdin and the gRPC stream pump parks forever on its next write")
	captured.StdinCleanup()
	assert.True(t, released,
		"the spec must carry the CALLER's cleanup, not a substitute — only the layer that created the reader knows whether closing it is legal")
}

// TestExecuteCLI_NonInteractiveCarriesNoStdinCleanup is the other half, and it
// is a decision rather than an omission: without a pty the reader is handed
// straight to the child and drained to EOF, so no copier goroutine ever parks
// on it and no writer waits on a reader that left. Supplying a cleanup here
// would close a reader that is still legitimately in use.
func TestExecuteCLI_NonInteractiveCarriesNoStdinCleanup(t *testing.T) {
	b, captured := newSpecCapturingBackend()

	_, err := b.ExecuteCLI(context.Background(), &ExecuteRequest{
		Mode:         ModeOneshot,
		StdinCleanup: func() { t.Error("a non-interactive run must never release the caller's stdin") },
	}, nil, bytes.NewReader(nil), nil, io.Discard, io.Discard)
	require.NoError(t, err)

	assert.Nil(t, captured.StdinCleanup,
		"a non-interactive launch owns no pty copier, so there is nothing to release and no reader it may close")
}

// ---- cell path: the engine home root ----------------------------------------
