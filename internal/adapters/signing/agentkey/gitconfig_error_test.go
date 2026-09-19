package agentkey

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecGitConfig_NoStderr_MessageIsSelfDescribing pins the invariant that
// execGitConfig's error is readable on its own: it always names the git config
// key it was resolving, and it never opens with a bare separator.
//
// The defect this pins: the wrap was an unconditional `"%s: %w"` over
// stderr, and git contributes NO stderr when the failure is on the ctxloom
// side of exec — a bad working directory means the child never starts, so
// stderr is empty and the exit error is not an *exec.ExitError at all. The
// user got ": fork/exec ...: chdir ...: no such file or directory", a
// dangling leading colon in front of a message that never mentions git
// config or the key.
func TestExecGitConfig_NoStderr_MessageIsSelfDescribing(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "no-such-dir")

	_, ok, err := execGitConfig(context.Background(), missingDir, "user.signingkey")

	require.Error(t, err, "an unrunnable git must be an error, not a silent 'unset'")
	assert.False(t, ok)
	assert.False(t, strings.HasPrefix(err.Error(), ":"),
		"error must not open with a dangling separator: %q", err.Error())
	assert.Contains(t, err.Error(), "user.signingkey",
		"error must name the key being resolved so it is readable without a stack: %q", err.Error())
}
