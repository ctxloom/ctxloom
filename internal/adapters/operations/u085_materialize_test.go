package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// emptyContextMaterializeFixture is materializeFixture with every source of
// context removed: the profile selects a tag nothing carries (and the fixture
// reads no companion loadout), so AssembleContext resolves cleanly to "".
func emptyContextMaterializeFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	cfg, target := materializeFixture(t, "UNSELECTED-CONTENT")
	// Re-seed the fixture's profile so it selects a tag no fragment carries.
	// materializeFixture writes "reviewer"; overwriting it here is what makes
	// the assembled context empty — under the gate that reads the real
	// rejections just recorded.
	return withProfileDefs(t, cfg, map[string]config.Profile{
		"reviewer": {SelectTags: []string{"no-fragment-carries-this-tag"}},
	}), target
}

// TestMaterializeProfile_RefusesEmptyAssembledContext pins this
// project's signature silent no-op: measured pre-fix, MaterializeProfile
// returned a nil error, reported Wrote: [context mcp settings commands skills]
// — naming the context surface — and wrote NO CLAUDE.md at all. Zero bytes, a
// success message, and a --target tree whose entire specialization is missing.
// The function's own doc already calls the assembled context "the core payload
// every native surface is built from" and "the one HARD-error surface"; an
// assembly that resolves to nothing is a failed assembly, exactly as
// the launch resolver rules for a named profile set that assembles to nothing.
func TestMaterializeProfile_RefusesEmptyAssembledContext(t *testing.T) {
	cfg, target := emptyContextMaterializeFixture(t)

	asm, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: []string{"reviewer"}})
	require.NoError(t, err)
	require.Empty(t, asm.Context, "the fixture must genuinely reach an empty assembled context")

	res, err := MaterializeProfile(context.Background(), engines.Registry(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"},
		Target:   target,
	})
	require.Error(t, err, "materializing a named profile set that assembles to nothing must fail loudly")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "reviewer", "the error must name the profile set that assembled to nothing")
	assert.NoFileExists(t, filepath.Join(target, "CLAUDE.md"))
}
