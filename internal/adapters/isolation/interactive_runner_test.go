package isolation

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInteractiveRunner_Host_IsTheSelfExecdRunner: a host cell's interactive
// runner is the same binary self-exec'd as `runner <engine>`, with the trio on
// its env; there is no container to name.
func TestInteractiveRunner_Host_IsTheSelfExecdRunner(t *testing.T) {
	for _, p := range []Policy{None{}, Worktree{}} {
		cmd, name, err := p.InteractiveRunner(context.Background(), "mock", hostWorkspace{dir: "/proj"}, map[string]string{"CTXLOOM_RUN_ID": "run-1"})
		require.NoError(t, err, p.Name())
		assert.Empty(t, name, "%s: nothing to remove by name", p.Name())
		assert.Equal(t, []string{"runner", "mock"}, cmd.Args[1:], p.Name())
		assert.Contains(t, strings.Join(cmd.Env, "\n"), "CTXLOOM_RUN_ID=run-1", p.Name())
		assert.Contains(t, strings.Join(cmd.Env, "\n"), "TERM="+RunnerTerm, p.Name())
	}
}
