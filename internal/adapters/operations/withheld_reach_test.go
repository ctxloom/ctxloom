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

// TestWithholds_ReachTheAgentsStartupFindings: a delivery withhold reaches
// the agent. Each case runs a real assembly reporting through the production
// sink and proves the withhold happened (its stderr warning); the package's
// withheld tally then rides into the startup findings a launch hands the
// agent (StartupFindings), naming the item and why. None is ledgered: every
// ledgered finding is fatal in strict mode, and a withhold must not abort.
func TestWithholds_ReachTheAgentsStartupFindings(t *testing.T) {
	for _, c := range []struct {
		name     string
		fixture  func(*testing.T) *config.Config
		profiles []string
		item     string
		stderr   string
		why      string
	}{
		{"a skill linked to an MCP server the run was not granted (exclude_mcp)", linkedSkillAndCommandFixture, []string{"without"},
			"linked#skills/reason", "which this run was not granted", `to MCP server "think", which this run was not granted`},
		{"a skill whose package does not parse", malformedSkillFixture, []string{"dev"},
			"#skills/half", `skill "half" withheld`, "its package did not load"},
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
				require.NotContains(t, s.TrustRef, c.item, "the skill was delivered after all")
			}

			recorded := strictness.Since(strictness.Mark{})
			for _, f := range recorded {
				assert.NotContains(t, f.Text, c.item, "a withhold is never ledgered: in strict mode that would abort the launch")
			}
			row := withheldRow(t, StartupFindings(&App{NoCompanions: true}, cfg, isolatedHome(t), recorded, pkg.Attestation().Withheld))
			assert.Contains(t, row.Detail, c.item, "the agent is told WHAT was withheld")
			assert.Contains(t, row.Detail, c.why, "the agent is told WHY")
		})
	}
}

// withheldRow is the one withheld-items row of a startup-findings report.
func withheldRow(t *testing.T, report DoctorReport) DoctorCheck {
	t.Helper()
	var rows []DoctorCheck
	for _, c := range report.Checks {
		if c.Marker == withheldItemsMarker {
			rows = append(rows, c)
		}
	}
	require.Len(t, rows, 1, "every withheld item is listed in ONE row")
	return rows[0]
}
