package coord

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// countingSources wraps the real reader and counts its Reads: the gate is
// stated in how many times a spawn consults the sources, so the count is the
// assertion and everything else is the real path.
type countingSources struct {
	config.Sources
	reads atomic.Int32
}

func (c *countingSources) Read(ctx context.Context) (*config.Config, []config.Warning, error) {
	c.reads.Add(1)
	return c.Sources.Read(ctx)
}

// TestProdSpawner_Resolve_OneSnapshotPerSpawn is the slice's "one snapshot
// per spawn" gate: every agent_run reloads the configuration exactly ONCE —
// so an agent definition edited mid-session takes effect on the NEXT spawn —
// and never twice or not at all. The memoized loader made it 0 or N.
func TestProdSpawner_Resolve_OneSnapshotPerSpawn(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: plan\n")

	inner, err := configload.New(nil, nil, configload.WithAppDir(appDir))
	require.NoError(t, err)
	src := &countingSources{Sources: inner}
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	src.reads.Store(0)

	s := newProdSpawner(operations.OpenedApp(owner), filepath.Dir(appDir), nil)

	_, err = s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, int32(1), src.reads.Load(), "one spawn is exactly one Read")

	_, err = s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, int32(2), src.reads.Load(), "the next spawn is the next Read, never a memo hit")

	// The edited definition is visible on the NEXT spawn and never before:
	// the snapshot published by the second spawn's Reload is what the
	// third spawn's Resolve reads from the owner after its own Reload.
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: plan\n  fresh:\n    llm: claude-code\n    permissions: bypass\n")
	plan, err := s.Resolve(context.Background(), "fresh")
	require.NoError(t, err, "an agent added mid-session resolves on the next spawn")
	assert.Equal(t, "claude-code", plan.Backend)
	assert.Equal(t, int32(3), src.reads.Load())
}
