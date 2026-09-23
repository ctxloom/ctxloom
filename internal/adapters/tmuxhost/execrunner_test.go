package tmuxhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapLookupTmux points NewExecRunner's probe at a stub for one test.
func swapLookupTmux(t *testing.T, lookup func() (string, error)) {
	t.Helper()
	old := lookupTmux
	lookupTmux = lookup
	t.Cleanup(func() { lookupTmux = old })
}

// A host without tmux is refused when the runner is BUILT, with an error a
// caller can match by identity -- not at the first tmux call inside a run.
func TestNewExecRunner_WithoutTmuxRefusesAtConstructionWithTypedError(t *testing.T) {
	lookErr := errors.New(`exec: "tmux": executable file not found in $PATH`)
	swapLookupTmux(t, func() (string, error) { return "", lookErr })

	_, err := NewExecRunner()

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTmuxUnavailable)
	assert.ErrorIs(t, err, lookErr, "the lookup's own failure must survive for the caller to report")
}

// The runner execs the binary the probe RESOLVED, not a bare "tmux" against
// the process PATH. The two diverge exactly where the probe earns its keep: a
// GUI-launched process whose PATH lacks the directory the login shell adds
// (Homebrew's, on macOS). The process PATH is emptied here so a runner that
// re-resolved "tmux" itself would fail.
func TestNewExecRunner_ExecsTheResolvedBinary(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux-elsewhere")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755))
	swapLookupTmux(t, func() (string, error) { return fake, nil })
	t.Setenv("PATH", t.TempDir())

	r, err := NewExecRunner()
	require.NoError(t, err)

	out, err := r.Run(context.Background(), "list-sessions")
	require.NoError(t, err)
	assert.Equal(t, "-L "+tmuxSocketName+" list-sessions\n", out)
}
