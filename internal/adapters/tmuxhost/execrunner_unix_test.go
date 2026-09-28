//go:build !windows

// tmux hosting is POSIX-only (see findTmux's Windows refusal).

package tmuxhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
