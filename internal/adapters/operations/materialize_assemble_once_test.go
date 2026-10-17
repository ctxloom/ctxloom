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

// TestMaterialize_ResolvesTheDefaultProfilesOnce pins that one engine's
// materialize resolves the default profile set ONCE: its context, settings
// and every other kind are written from one assembled package. Each resolution of the default set
// reports a default profile that does not resolve (a KindRef finding), so the
// count of those findings is the count of resolutions.
func TestMaterialize_ResolvesTheDefaultProfilesOnce(t *testing.T) {
	ownRecordsDir(t)
	resetStrictness(t)
	tmpDir := t.TempDir()
	writeBundleFixture(t, tmpDir)

	const missingProfile = "not-installed"
	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{filepath.Join(tmpDir, ".ctxloom")},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"test", missingProfile}}},
	})

	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })
	_, err := Materialize(context.Background(), engines.Registry(), cfg, MaterializeRequest{
		Engines: []string{"claude-code"}, Target: tmpDir,
		// Degraded, so the delivery writes past the finding the count reads.
		Strictness: strictness.Mode{Prog: "ctxloom", Degraded: true},
	})
	require.NoError(t, err)

	var resolutions int
	for _, f := range strictness.Since(mark) {
		if f.Kind == report.KindRef {
			resolutions++
			assert.NotContains(t, f.Remedy, "deps pull", "a pull cannot create a missing LOCAL profile")
		}
	}
	assert.Equal(t, 1, resolutions, "one engine's materialize, one resolution of the default profile set")
}
