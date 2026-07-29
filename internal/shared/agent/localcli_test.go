package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyLocalCLIConfig_NilEnvDoesNotPanic pins U101-F19: ApplyLocalCLIConfig
// assigned into b.Env unconditionally, so a BaseBackend constructed without
// going through NewBaseBackend (whose Env: make(map[string]string) is the only
// thing that made this safe) panicked on "assignment to entry in nil map" the
// moment a caller passed a non-empty env override.
func TestApplyLocalCLIConfig_NilEnvDoesNotPanic(t *testing.T) {
	b := &BaseBackend{} // zero value: Env is nil, unlike NewBaseBackend's

	require.NotPanics(t, func() {
		ApplyLocalCLIConfig(b, "", nil, map[string]string{"FOO": "bar"})
	})
	assert.Equal(t, "bar", b.Env["FOO"])
}
