package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// writeLinkedBundleFixture lays down one bundle whose skill, command and
// session_start hook are linked to the MCP server they drive, plus two
// profiles over it: "with" grants the server, "without" vetoes it with
// exclude_mcp. Same bundle, same items, two runs.
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
hooks:
  session_start:
    - command: think-warmup
      tags: [ctxloom:link_id=think]
  pre_tool:
    - command: free-guard
`)
	require.NoError(t, os.MkdirAll(filepath.Join(bundlesDir, "linked", "skills", "free"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: reason\ndescription: Drives the think server.\n---\n\nUse think.\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "linked", "skills", "free", "SKILL.md"),
		[]byte("---\nname: free\ndescription: Needs nothing.\n---\n\nBody.\n"), 0644))

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"with"}}},
		appPaths: []string{appDir},
	}
	cfg.BindTrustForTesting(compositetest.Trust())
	return cfg
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
	t.Skip("unexpressible: a bundle is a tree, and the tree's MCP and hook sidecars carry no tags, so neither can declare ctxloom:link_id — raised with the human (unruly-frostbite) as a tree-format decision")
	cfg := writeLinkedBundleFixture(t)

	require.Contains(t, cfg.ResolveBundleMCPServers([]string{"with"}), "think")
	require.NotContains(t, cfg.ResolveBundleMCPServers([]string{"without"}), "think")

	assert.ElementsMatch(t, []string{"free", "reason"}, skillNames(t, cfg, []string{"with"}))
	assert.Equal(t, []string{"plan"}, commandNames(t, cfg, []string{"with"}))

	assert.Equal(t, []string{"free"}, skillNames(t, cfg, []string{"without"}),
		"the linked skill is withheld with its vetoed server; the unlinked one is not collateral")
	assert.Empty(t, commandNames(t, cfg, []string{"without"}))
}

func hookCommands(hooks []wire.Hook) []string {
	var out []string
	for _, h := range hooks {
		out = append(out, h.Command)
	}
	return out
}

// A HOOK IS A MEMBER TOO. A session_start hook that calls a tool its server
// provides is the exact shape link groups exist to stop: delivered beside a
// withheld server it fires against nothing, silently. So the hook delivers
// exactly when the server it is linked to is granted, and the unlinked hook
// beside it is never collateral.
func TestConfig_ResolveBundleHooks_LinkedHookFollowsTheRunsGrantedMCPSet(t *testing.T) {
	t.Skip("unexpressible: a bundle is a tree, and the tree's MCP and hook sidecars carry no tags, so neither can declare ctxloom:link_id — raised with the human (unruly-frostbite) as a tree-format decision")
	cfg := writeLinkedBundleFixture(t)

	require.Contains(t, cfg.ResolveBundleMCPServers([]string{"with"}), "think")
	with := cfg.ResolveBundleHooks([]string{"with"})
	assert.Equal(t, []string{"think-warmup"}, hookCommands(with.SessionStart))
	assert.Equal(t, []string{"free-guard"}, hookCommands(with.PreTool))

	require.NotContains(t, cfg.ResolveBundleMCPServers([]string{"without"}), "think")
	without := cfg.ResolveBundleHooks([]string{"without"})
	assert.Empty(t, hookCommands(without.SessionStart),
		"the linked hook is withheld with its vetoed server: a hook must never fire against a tool that is not there")
	assert.Equal(t, []string{"free-guard"}, hookCommands(without.PreTool),
		"the unlinked hook is not collateral")
}

// The hook path follows the pipeline's rule for an omitted grant: nil fails
// CLOSED for every linked hook and touches no unlinked one; not checking is
// spelled bundles.LinksUnchecked, out loud.
func TestExtractHooksFromBundle_NilLinkGrantWithholdsLinkedHooksOnly(t *testing.T) {
	link := []string{"ctxloom:link_id=think"}
	b := &bundles.Bundle{
		MCP: map[string]bundles.BundleMCP{"think": {Command: "think-server", Tags: link}},
		Hooks: bundles.BundleHooks{
			SessionStart: []bundles.BundleHook{{Command: "think-warmup", Tags: link}},
			PreTool:      []bundles.BundleHook{{Command: "free-guard"}},
		},
	}
	read := bundles.ProjectAuthoredRead("fixture", b)

	got := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), composite.Ungated().Authorizer(), nil)
	assert.Empty(t, hookCommands(got.SessionStart))
	assert.Equal(t, []string{"free-guard"}, hookCommands(got.PreTool))

	unchecked := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), composite.Ungated().Authorizer(), bundles.LinksUnchecked())
	assert.Equal(t, []string{"think-warmup"}, hookCommands(unchecked.SessionStart))
}

// The grant keys on the server AS SHIPPED BY THE BUNDLE, not on the bare name.
// When two bundles both declare `think`, the name arbiter withholds the later
// claim; a same-named server delivered from the incumbent must not stand in
// for the loser's, so the loser's linked command is withheld even though a
// server called `think` is in the granted set.
func TestConfig_LinkGrant_RequiresTheOwningBundlesServer(t *testing.T) {
	t.Skip("unexpressible: a bundle is a tree, and the tree's MCP and hook sidecars carry no tags, so neither can declare ctxloom:link_id — raised with the human (unruly-frostbite) as a tree-format decision")
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

// TestConfig_LinkGrant_ResolvesLazily: building a grant must not itself walk
// the profiles. Construction happens inside every pipeline the run builds —
// context assembly, skills, commands, curated exports — and the resolve it
// wraps records a strictness finding for each unresolvable ref. Resolving at
// construction therefore re-records the run's own findings once per pipeline,
// and `ctxloom doctor`, which COUNTS ClassRef findings around one
// AssembleContext call to report how many refs were skipped, then reports
// double. The grant resolves on the first question asked of it, never before,
// and exactly once.
func TestConfig_LinkGrant_ResolvesLazily(t *testing.T) {
	resetStrictness(t)
	f := Fixture{AppPaths: []string{t.TempDir()}}
	f.DefaultAgent = "default"
	f.Agents = map[string]agents.Agent{
		"default": {Profiles: []string{"link-lazy-missing-one", "link-lazy-missing-two"}},
	}
	cfg := NewFixture(f)
	cfg.rep = ledgerReporter()

	mark := strictness.Checkpoint()
	grant := cfg.LinkGrant([]string{"link-lazy-missing-one", "link-lazy-missing-two"})
	require.Empty(t, refFindings(strictness.Since(mark)),
		"constructing a grant must record nothing: the resolve it wraps is deferred to the first question")

	grant.Granted(bundles.BundleRead{}, "anything")
	first := len(refFindings(strictness.Since(mark)))
	require.Equal(t, 2, first, "the first question resolves once and records each unresolvable ref once")

	grant.Granted(bundles.BundleRead{}, "anything-else")
	assert.Equal(t, first, len(refFindings(strictness.Since(mark))),
		"a second question re-uses the resolved set and records nothing more")
}

func refFindings(fs []report.Finding) []report.Finding {
	var out []report.Finding
	for _, f := range fs {
		if f.Kind == report.KindRef {
			out = append(out, f)
		}
	}
	return out
}
