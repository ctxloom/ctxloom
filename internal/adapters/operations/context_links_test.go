package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// A fragment linked to an MCP server is assembled into a run's context only
// when that run was granted the server. The two profiles below reach the same
// bundle; "without" vetoes the server, so its assembly must not carry the
// fragment — and must still carry the bundle's unlinked fragment.
func TestAssembleContext_LinkedFragmentFollowsTheRunsGrantedMCPSet(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, paths.AppDirName)
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "with.yaml"),
		[]byte("bundles:\n  - linked\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "without.yaml"),
		[]byte("bundles:\n  - linked\nexclude_mcp:\n  - think\n"), 0644))

	bundleDir := authoredV1(appDir)
	require.NoError(t, os.MkdirAll(bundleDir, 0755))
	bundletree.WriteOS(t, bundleDir, "linked", `version: "1.0"
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=think]
fragments:
  guide:
    content: "LINKED-GUIDE"
    tags: [ctxloom:link_id=think]
  plain:
    content: "PLAIN-FRAGMENT"
`)

	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"with"}}},
	})

	with, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: []string{"with"}})
	require.NoError(t, err)
	assert.Contains(t, with.Context, "LINKED-GUIDE")
	assert.Contains(t, with.Context, "PLAIN-FRAGMENT")

	without, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: []string{"without"}})
	require.NoError(t, err)
	assert.NotContains(t, without.Context, "LINKED-GUIDE", "the fragment is withheld with the server it is linked to")
	assert.Contains(t, without.Context, "PLAIN-FRAGMENT", "the unlinked fragment is not collateral")
	assert.NotContains(t, without.FragmentsLoaded, "linked/guide")
}

// linkedSkillAndCommandFixture lays down one bundle whose skill and command
// are linked to the MCP server they drive, beside an unlinked skill, and two
// profiles over it: "with" grants the server, "without" vetoes it.
func linkedSkillAndCommandFixture(t *testing.T) *config.Config {
	t.Helper()
	appDir, bundlesDir := scopeFixture(t, nil)
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "with.yaml"),
		[]byte("bundles:\n  - linked\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "without.yaml"),
		[]byte("bundles:\n  - linked\nexclude_mcp:\n  - think\n"), 0o644))
	for _, skill := range []string{"reason", "free"} {
		dir := filepath.Join(bundlesDir, "linked", "skills", skill)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: "+skill+"\ndescription: A skill.\n---\n\nBody.\n"), 0o644))
	}
	bundletree.WriteOS(t, bundlesDir, "linked", `version: "1.0"
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
`)
	return defaultsTo(appDir, "with")
}

// A skill or command linked to an MCP server is delivered exactly when the
// run is granted that server: the run's OWN granted set, over the profiles it
// selected, decides — and an unlinked skill beside it is never collateral.
func TestAssemblePackage_LinkedSkillAndCommandFollowTheRunsGrantedMCPSet(t *testing.T) {
	cfg := linkedSkillAndCommandFixture(t)

	assert.ElementsMatch(t, []string{"free", "reason"}, skillItemNames(skillsOf(t, cfg, []string{"with"})))
	assert.Equal(t, []string{"plan"}, bundlePromptItems(commandsOf(t, cfg, []string{"with"})))

	assert.Equal(t, []string{"free"}, skillItemNames(skillsOf(t, cfg, []string{"without"})),
		"the linked skill is withheld with its vetoed server; the unlinked one is not collateral")
	assert.Empty(t, bundlePromptItems(commandsOf(t, cfg, []string{"without"})),
		"the linked command is withheld with its vetoed server")
}

// The grant keys on the server AS SHIPPED BY THE OWNING BUNDLE, not on the
// bare name. When two bundles both declare `think`, the name arbiter keeps
// the incumbent's; that same-named server must not stand in for the loser's,
// so the loser's linked command is withheld although a server called `think`
// was granted.
func TestAssemblePackage_LinkGrantRequiresTheOwningBundlesServer(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	appDir, bundlesDir := scopeFixture(t, map[string]string{"first": "incumbent", "second": "loser"})
	bundletree.WriteOS(t, bundlesDir, "incumbent", "version: \"1.0\"\nmcp:\n  think:\n    command: winner-think\n")
	bundletree.WriteOS(t, bundlesDir, "loser", `version: "1.0"
mcp:
  think:
    command: loser-think
    tags: [ctxloom:link_id=think]
commands:
  plan:
    content: PLAN
    tags: [ctxloom:link_id=think]
`)
	cfg := defaultsTo(appDir)

	pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: []string{"first", "second"}})
	require.NoError(t, err)
	require.Equal(t, "winner-think", pkg.MCP["think"].Command, "the fixture must produce a contest the loser loses")
	assert.Empty(t, bundlePromptItems(commandsOf(t, cfg, []string{"first", "second"})),
		"the loser's linked command is withheld: its own server was not granted, whatever answers to the name")

	assert.Equal(t, []string{"plan"}, bundlePromptItems(commandsOf(t, cfg, []string{"second"})),
		"alone, the same bundle's server is granted and the command delivers")
}
