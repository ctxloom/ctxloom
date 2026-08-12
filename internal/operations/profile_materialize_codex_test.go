package operations

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestMaterializeProfile_CodexDeclaresItsLaunchOnlySurfaces is S7's
// materialize-side claim, and it has two halves that must both hold.
//
// The LOSS half: codex's settings (hooks + MCP servers), prompts and skills
// live only in $CODEX_HOME, and the only $CODEX_HOME ctxloom writes is the
// per-session one an agent launch creates — so a harpless materialize writes
// none of them and must SAY so. Silence here is the exact shape whiny-exclusive
// closed for opencode: four true "wrote" lines and a team's guardrail dropped
// with no line anywhere.
//
// The DELIVERY half: codex's cwd-keyed AGENTS.md is untouched by the
// declaration and still lands with the assembled context in it. Without that
// assertion this test is satisfied by a materialize that did nothing at all,
// which is the opposite of the narrowing S7 actually made.
func TestMaterializeProfile_CodexDeclaresItsLaunchOnlySurfaces(t *testing.T) {
	cfg, target := materializeHookFixture(t)

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"}, Target: target, Backend: "codex",
	})
	require.NoError(t, err, "a declared absence is a SKIP, not a failure: the rest of the tree still ships")

	// The loss half, per surface, each carrying the ONE declared reason.
	lost := map[string]agent.SurfaceLoss{}
	for _, l := range res.NotCarried {
		lost[l.Surface] = l
	}
	for _, surface := range []string{"hooks", "mcp"} {
		loss, ok := lost[surface]
		require.True(t, ok, "codex's %s surface has no durable project home and must be reported as not carried; got %v", surface, res.NotCarried)
		assert.Contains(t, loss.Reason, "delivered per-session at launch",
			"a loss with no stated destination reads as a loss, not a narrowing")
		assert.NotEmpty(t, loss.Detail, "the report must name WHAT was not carried, not just that something was")
	}
	assert.Contains(t, lost["hooks"].Detail, "session_start",
		"the hook loss must name the event the team configured")

	// The delivery half — the vacuity guard on everything above.
	data, err := os.ReadFile(filepath.Join(target, "AGENTS.md"))
	require.NoError(t, err, "codex's cwd-keyed context surface is untouched by the declaration")
	assert.Contains(t, string(data), "HOOKED-CONTENT",
		"an empty AGENTS.md would make the absences above vacuous")

	// And nothing home-keyed anywhere under the target: asserting one guessed
	// path would pass a fallback that moved sideways.
	require.NoError(t, filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		assert.NotContains(t, path, ".codex",
			"a harpless materialize has no codex home to write %s into", path)
		return nil
	}))
}

// TestMaterializeProfile_OtherEnginesUnaffectedByCodexDeclaration is the
// cross-engine vacuity guard. The declared absence is codex's alone: a
// regression that applied it by backend-name typo, or that hoisted the check up
// into the shared materialize path, would silently empty every engine's tree
// while every "not carried" assertion above still passed.
func TestMaterializeProfile_OtherEnginesUnaffectedByCodexDeclaration(t *testing.T) {
	for _, tc := range []struct {
		backend, file, marker string
	}{
		{"claude-code", filepath.Join(".claude", "settings.json"), "team-guardrail"},
		{"kiro", filepath.Join(".kiro", "agents", "ctxloom.json"), "team-guardrail"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			cfg, target := materializeHookFixture(t)

			res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
				Profiles: []string{"reviewer"}, Target: target, Backend: tc.backend,
			})
			require.NoError(t, err)
			assert.Empty(t, res.NotCarried, "%s carries its own settings surface; nothing is launch-only for it", tc.backend)

			data, rerr := os.ReadFile(filepath.Join(target, tc.file))
			require.NoError(t, rerr, "%s's settings surface must still be written", tc.backend)
			assert.Contains(t, string(data), tc.marker, "and must still carry the team's hook")
		})
	}
}
