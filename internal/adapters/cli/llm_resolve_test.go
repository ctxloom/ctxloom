package cli

import (
	"io"
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines/mock"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// fn wrote to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// TestIsMockBackend pins the one predicate both usableLLMs (run.go) and
// getAvailableEngines (init.go) now share: the "mock"
// backend must never surface in a user-facing engine list, and nothing else
// should be caught by the same check.
func TestIsTestOnlyBackend(t *testing.T) {
	assert.True(t, isTestOnlyBackend(string(mock.Name)))
	assert.True(t, isTestOnlyBackend(string(mock.NameLossy)),
		"a SECOND double must be hidden too — that is the whole reason this asks the registry instead of matching a name")
	assert.False(t, isTestOnlyBackend("claude-code"))
	assert.False(t, isTestOnlyBackend(""), "an unknown name is not test-only: a typo must not read as a hidden engine")
}
