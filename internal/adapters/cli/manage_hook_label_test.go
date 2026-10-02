package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// TestResolvedHookLabel_NamesAnExecHooksWholeArgv: an exec-form hook is
// listed as what runs — its executable and every argument — not as the bare
// executable every ctxloom hook shares.
func TestResolvedHookLabel_NamesAnExecHooksWholeArgv(t *testing.T) {
	assert.Equal(t, `'ctxloom' 'hook' 'next-step'`, resolvedHookLabel(operations.ResolvedHook{Command: "ctxloom", Args: []string{"hook", "next-step"}}))
	assert.Equal(t, `./check.sh`, resolvedHookLabel(operations.ResolvedHook{Command: "./check.sh"}))
}
