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

// ---- ExecuteCLI: the terminal relay ------------------------------------------

// newSpecCapturingBackend returns a LaunchBackend whose launcher records the
// LaunchSpec it was handed instead of execing anything, so a test can read what
// ExecuteCLI actually assembled for the runtime.
func newSpecCapturingBackend() (*LaunchBackend, *LaunchSpec) {
	var captured LaunchSpec
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.SetLauncher(func(_ context.Context, spec LaunchSpec, _ io.Reader, _, _ io.Writer) (int32, error) {
		captured = spec
		return 0, nil
	})
	return b, &captured
}

// TestExecuteCLI_InteractiveRunsOnTheRequestsTerminal pins the middle of the
// terminal's chain: the runner's own stdio travels stdioTerminal →
// ExecuteRequest.Terminal → ExecuteCLI → RunInteractive → LaunchSpec →
// runner.RunLaunchSpec, which hands it to the engine. ExecuteCLI is the shared
// exec tail every exec-style backend funnels through, so the spec must carry
// the CALLER's terminal — not a substitute, and not nothing, which the
// launcher would refuse.
func TestExecuteCLI_InteractiveRunsOnTheRequestsTerminal(t *testing.T) {
	b, captured := newSpecCapturingBackend()
	term := &Terminal{}

	_, err := b.ExecuteCLI(context.Background(), &ExecuteRequest{Mode: ModeInteractive, Terminal: term}, "/bin/true", nil, nil, nil, io.Discard, io.Discard)
	require.NoError(t, err)

	assert.True(t, captured.Interactive, "an interactive request launches interactively")
	assert.Same(t, term, captured.Terminal, "the engine runs on the caller's terminal")
}

// TestExecuteCLI_NonInteractiveCarriesNoTerminal is the other half: a
// non-interactive launch runs over pipes, and a terminal on its spec would
// say otherwise.
func TestExecuteCLI_NonInteractiveCarriesNoTerminal(t *testing.T) {
	b, captured := newSpecCapturingBackend()

	_, err := b.ExecuteCLI(context.Background(), &ExecuteRequest{Mode: ModeOneshot, Terminal: &Terminal{}}, "/bin/true", nil, bytes.NewReader(nil), nil, io.Discard, io.Discard)
	require.NoError(t, err)

	assert.False(t, captured.Interactive)
	assert.Nil(t, captured.Terminal, "a non-interactive launch runs over pipes, never on a terminal")
}

// ---- cell path: the engine home root ----------------------------------------
