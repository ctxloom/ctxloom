package backends

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// U057-F02: a builtin slash command that failed to load used to be dropped
// with `continue` and zero diagnostics. Because the command writers reconcile
// by removing every ctxloom-managed file then re-adding the assembled set, a
// silently dropped builtin simply vanishes from the next materialize with no
// signal at all. It must now warn.
func TestBuiltinCommands_LoadFailureIsWarned(t *testing.T) {
	orig := getBuiltinCommandFn
	getBuiltinCommandFn = func(name string) ([]byte, error) {
		return nil, fmt.Errorf("embedded read exploded")
	}
	defer func() { getBuiltinCommandFn = orig }()

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	prompts := builtinCommands()
	assert.Empty(t, prompts, "every builtin failed to load, so nothing is returned")
	assert.Contains(t, buf.String(), "unavailable", "a failed builtin command load must be warned, not silently dropped")
}

// TestBuiltinCommands_RealResourcesLoad is a smoke test that the real
// embedded resources still load through the seam unchanged (guards against
// the seam accidentally becoming the only path exercised by tests).
func TestBuiltinCommands_RealResourcesLoad(t *testing.T) {
	prompts := builtinCommands()
	require.NotEmpty(t, prompts, "the real embedded builtin commands must still load")
}
