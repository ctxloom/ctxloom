package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestSyncOnStartup_RefreshesClonesBeforeProbe pins a fix: the
// referenced-clone refresh runs BEFORE the missing-dependency probe, so a
// steady-state startup (everything installed → Count 0 → "up_to_date"
// short-circuit) still advances the clone cache. Previously the only refresh
// lived inside SyncDependencies, which the short-circuit made unreachable —
// a project's clone cache could go stale indefinitely.
func TestSyncOnStartup_RefreshesClonesBeforeProbe(t *testing.T) {
	refreshed := false
	orig := startupCloneRefresh
	startupCloneRefresh = func(ctx context.Context, cfg *config.Config) { refreshed = true }
	t.Cleanup(func() { startupCloneRefresh = orig })

	// An empty config has no remote references: the probe reports Count 0 and
	// SyncOnStartup short-circuits — exactly the steady state that used to skip
	// the refresh.
	cfg := &config.Config{}
	res, err := SyncOnStartup(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "up_to_date", res.Status, "steady state short-circuits")
	assert.True(t, refreshed, "the clone refresh must run even on the short-circuit path")
}
