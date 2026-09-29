package tmuxhost

import (
	"errors"
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
