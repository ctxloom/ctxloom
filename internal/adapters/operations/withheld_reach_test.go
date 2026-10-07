package operations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// malformedSkillFixture is one profile over a bundle declaring a skill whose
// SKILL.md has no frontmatter, so the package cannot be parsed.
func malformedSkillFixture(t *testing.T) *config.Config {
	t.Helper()
	appDir, bundlesDir := scopeFixture(t, map[string]string{"dev": "sb"})
	dir := filepath.Join(bundlesDir, "sb", "skills", "half")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("a body with no frontmatter\n"), 0o644))
	bundletree.WriteOS(t, bundlesDir, "sb", "version: \"1.0\"\nskills:\n  half: {}\n")
	return defaultsTo(appDir, "dev")
}

// TestWithholds_ReachStderrButNotTheAgent pins where a delivery withhold is
// voiced today. Each case runs a real assembly reporting through the
// production sink, proves the withhold happened (its stderr warning), then
// looks where a launched agent would see it: the startup findings a launch
// hands the agent are built from the strictness ledger (StartupFindings), and
// the ledger holds only fatal-class findings. These withholds are advisories,
// so the agent gets a roster with the item missing and nothing saying why.
//
// When a withhold is made to reach the agent, its case here must flip.
func TestWithholds_ReachStderrButNotTheAgent(t *testing.T) {
	for _, c := range []struct {
		name     string
		fixture  func(*testing.T) *config.Config
		profiles []string
		item     string
		stderr   string
	}{
		{"a skill linked to an MCP server the run was not granted", linkedSkillAndCommandFixture, []string{"without"}, "linked#skills/reason", "which this run was not granted"},
		{"a skill whose package does not parse", malformedSkillFixture, []string{"dev"}, `"half"`, `skill "half" withheld`},
	} {
		t.Run(c.name, func(t *testing.T) {
			strictness.Reset()
			t.Cleanup(strictness.Reset)
			cfg := c.fixture(t)
			cfg.SetReporter(strictness.Sink("ctxloom"))
			var stderr bytes.Buffer
			t.Cleanup(clidiag.SetSink(&stderr))

			pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: c.profiles})
			require.NoError(t, err)
			require.Contains(t, stderr.String(), c.stderr, "the withhold must have happened for this case to mean anything")
			for _, s := range LoadedSkills(pkg) {
				require.NotContains(t, s.ItemRef, c.item, "the skill was delivered after all")
			}

			recorded := strictness.Since(strictness.Mark{})
			for _, f := range recorded {
				assert.NotContains(t, f.Text, c.item, "the withhold is ledgered now: flip this case")
			}
			for _, row := range StartupFindings(&App{NoCompanions: true}, cfg, isolatedHome(t), recorded).Checks {
				assert.NotContains(t, row.Detail, c.item, "the withhold reaches the agent's startup findings now: flip this case")
			}
		})
	}
}
