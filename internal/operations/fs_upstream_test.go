package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestShouldChainFsUpstream covers B5's (gap G14) "one rule" in isolation
// from the rest of OpenEngineSession's heavy machinery (config load, real
// isolation.Prepare): fs chains upstream ONLY on the fully unisolated host
// axis — never when a worktree was prepared (aw != nil), never when the
// runtime axis containerized the engine.
func TestShouldChainFsUpstream(t *testing.T) {
	tests := []struct {
		name        string
		aw          *acpWorkspace
		runtimeAxis string
		want        bool
	}{
		{
			name:        "host axis: no worktree, no container -> chains",
			aw:          nil,
			runtimeAxis: "",
			want:        true,
		},
		{
			name:        "worktree axis: never chains, regardless of runtime",
			aw:          &acpWorkspace{dir: "/some/worktree"},
			runtimeAxis: "",
			want:        false,
		},
		{
			name:        "rootless container axis: never chains, even with no worktree",
			aw:          nil,
			runtimeAxis: agent.RuntimeContainerRootless,
			want:        false,
		},
		{
			name:        "rootful container axis: never chains, even with no worktree",
			aw:          nil,
			runtimeAxis: agent.RuntimeContainerRootful,
			want:        false,
		},
		{
			name:        "worktree AND container: still never chains",
			aw:          &acpWorkspace{dir: "/some/worktree"},
			runtimeAxis: agent.RuntimeContainerRootless,
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldChainFsUpstream(tt.aw, tt.runtimeAxis))
		})
	}
}
