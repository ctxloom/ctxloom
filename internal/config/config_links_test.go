package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// writeLinkedBundleFixture lays down one bundle whose skill is linked to the
// MCP server it drives, plus two profiles over it: "with" grants the server,
// "without" vetoes it with exclude_mcp. Same bundle, same skill, two runs.
func writeLinkedBundleFixture(t *testing.T) *Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	skillDir := filepath.Join(bundlesDir, "linked", "skills", "reason")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(skillDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "with.yaml"),
		[]byte("name: with\nbundles:\n  - linked\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "without.yaml"),
		[]byte("name: without\nbundles:\n  - linked\nexclude_mcp:\n  - think\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "linked", "bundle.yaml"),
		[]byte(`version: "1.0"
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=think]
skills:
  reason:
    tags: [ctxloom:link_id=think]
  free: {}
commands:
  plan:
    content: PLAN
    tags: [ctxloom:link_id=think]
`), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(bundlesDir, "linked", "skills", "free"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: reason\ndescription: Drives the think server.\n---\n\nUse think.\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "linked", "skills", "free", "SKILL.md"),
		[]byte("---\nname: free\ndescription: Needs nothing.\n---\n\nBody.\n"), 0644))

	return &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"with"}}},
		appPaths: []string{appDir},
	}
}

func skillNames(t *testing.T, cfg *Config, profiles []string) []string {
	t.Helper()
	var names []string
	for _, s := range cfg.ResolveBundleSkills(profiles) {
		names = append(names, s.Item)
	}
	return names
}

func commandNames(t *testing.T, cfg *Config, profiles []string) []string {
	t.Helper()
	var names []string
	for _, c := range cfg.ResolveBundleCommands(profiles) {
		names = append(names, c.Item)
	}
	return names
}

// The grant is the run's OWN granted set — ResolveBundleMCPServers over the
// selected profiles — so a profile that vetoes the server withholds the skill
// and command linked to it, and a profile that grants it delivers them.
func TestConfig_LinkGrant_FollowsTheRunsGrantedMCPSet(t *testing.T) {
	cfg := writeLinkedBundleFixture(t)

	require.Contains(t, cfg.ResolveBundleMCPServers([]string{"with"}), "think")
	require.NotContains(t, cfg.ResolveBundleMCPServers([]string{"without"}), "think")

	assert.ElementsMatch(t, []string{"free", "reason"}, skillNames(t, cfg, []string{"with"}))
	assert.Equal(t, []string{"plan"}, commandNames(t, cfg, []string{"with"}))

	assert.Equal(t, []string{"free"}, skillNames(t, cfg, []string{"without"}),
		"the linked skill is withheld with its vetoed server; the unlinked one is not collateral")
	assert.Empty(t, commandNames(t, cfg, []string{"without"}))
}

// The grant keys on the server AS SHIPPED BY THE BUNDLE, not on the bare name.
// When two bundles both declare `think`, the name arbiter withholds the later
// claim; a same-named server delivered from the incumbent must not stand in
// for the loser's, so the loser's linked command is withheld even though a
// server called `think` is in the granted set.
func TestConfig_LinkGrant_RequiresTheOwningBundlesServer(t *testing.T) {
	resetStrictness(t)
	linked := `version: "1.0"
mcp:
  think:
    command: loser-think
    tags: [ctxloom:link_id=think]
commands:
  plan:
    content: PLAN
    tags: [ctxloom:link_id=think]
`
	cfg := mcpContestFixture(t,
		map[string]string{
			"incumbent": mcpBundleYAML([2]string{"think", "winner-think"}),
			"loser":     linked,
		},
		map[string]string{"first": "incumbent", "second": "loser"},
	)

	granted := cfg.ResolveBundleMCPServers([]string{"first", "second"})
	require.Equal(t, "winner-think", granted["think"].Command, "the fixture must produce a contest the loser loses")

	assert.Empty(t, commandNames(t, cfg, []string{"first", "second"}),
		"the loser's linked command is withheld: its own server was not granted, whatever answers to the name")
	assert.Equal(t, []string{"plan"}, commandNames(t, cfg, []string{"second"}),
		"alone, the same bundle's server is granted and the command delivers")
}
