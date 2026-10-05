package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestApplyHooks_ResolvesTheDefaultProfilesOnce pins that one ApplyHooks —
// `manage hooks install` with context regeneration — resolves the default
// profile set ONCE: the regenerated context and every backend's settings are
// written from one assembled package. Each resolution of the default set
// reports a default profile that does not resolve (a KindRef finding), so the
// count of those findings is the count of resolutions.
func TestApplyHooks_ResolvesTheDefaultProfilesOnce(t *testing.T) {
	resetStrictness(t)
	tmpDir := t.TempDir()
	writeBundleFixture(t, tmpDir)

	const missingProfile = "not-installed"
	cfg := realGated(gatedFixture(config.Fixture{
		AppPaths:     []string{filepath.Join(tmpDir, ".ctxloom")},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"test", missingProfile}}},
	}))

	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })
	_, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		Cfg:               cfg,
		WorkDir:           tmpDir,
	})
	require.NoError(t, err)

	var resolutions int
	for _, f := range strictness.Since(mark) {
		if f.Kind == report.KindRef {
			resolutions++
		}
	}
	assert.Equal(t, 1, resolutions, "one apply, one resolution of the default profile set")
}
