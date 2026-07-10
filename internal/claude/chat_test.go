package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChatACPConfig_StripsNestedSessionGuard is the regression pin for the
// agentcoord Wave B1 child-death: a delegated claude child's engine chain is
// spawned from inside the parent claude's process tree, inherits CLAUDECODE,
// and claude's nested-session guard then refuses to start — the child died at
// session/new with an opaque -32603 before its first turn. The chat driver
// must strip the guard variable for its deliberate, independent engine spawn.
func TestChatACPConfig_StripsNestedSessionGuard(t *testing.T) {
	cfg := chatACPConfig(map[string]string{"FOO": "bar"})
	assert.Equal(t, claudeACPAdapter, cfg.Command)
	assert.Equal(t, map[string]string{"FOO": "bar"}, cfg.Env, "backend env overlay passes through")
	assert.Contains(t, cfg.StripEnv, "CLAUDECODE", "the nested-session guard must not leak into the child engine")
}
