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
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport/admitall"
)

// writeLinkedBundleFixture lays down one bundle whose session_start hook is
// linked to the MCP server it drives, beside an unlinked pre_tool hook, plus
// two profiles over it: "with" grants the server, "without" vetoes it with
// exclude_mcp. Same bundle, same items, two runs.
func writeLinkedBundleFixture(t *testing.T) *Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "with.yaml"),
		[]byte("bundles:\n  - linked\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "without.yaml"),
		[]byte("bundles:\n  - linked\nexclude_mcp:\n  - think\n"), 0644))
	bundletree.WriteOS(t, bundlesDir, "linked", `version: "1.0"
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=think]
hooks:
  session_start:
    - command: think-warmup
      tags: [ctxloom:link_id=think]
  pre_tool:
    - command: free-guard
`)

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"with"}}},
		appPaths: []string{appDir},
	}
	cfg.BindTrustForTesting(compositetest.Trust())
	return cfg
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
	read := readWithHooks(t, bundles.BundleHooks{
		SessionStart: []bundles.BundleHook{{Command: "think-warmup", Tags: link}},
		PreTool:      []bundles.BundleHook{{Command: "free-guard"}},
	})

	got := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), admitall.Authorizer(), nil)
	assert.Empty(t, hookCommands(got.SessionStart))
	assert.Equal(t, []string{"free-guard"}, hookCommands(got.PreTool))

	unchecked := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), admitall.Authorizer(), bundles.LinksUnchecked())
	assert.Equal(t, []string{"think-warmup"}, hookCommands(unchecked.SessionStart))
}

// TestConfig_LinkGrantFor_ResolvesLazily: building a grant must not itself
// resolve the run's MCP servers. Construction happens inside every pipeline the
// run builds — context assembly, skills, commands, curated exports — and the
// resolve it wraps reports a finding for each unloadable bundle ref.
// The grant resolves on the first question asked of it, never before, and
// exactly once.
func TestConfig_LinkGrantFor_ResolvesLazily(t *testing.T) {
	cfg := NewFixture(Fixture{AppPaths: []string{t.TempDir()}})
	// Count every report raw, ahead of any sink-side dedup: a FailOnce
	// finding repeated by a second resolve is folded by the strictness
	// ledger, so only the raw stream shows whether the grant memoised.
	reported := 0
	cfg.rep = report.To(report.SinkFunc(func(f report.Finding) {
		if f.Kind == report.KindBundle {
			reported++
		}
	}))
	set := []profiles.ResolvedProfile{{
		Name:    "link-lazy",
		Bundles: []string{"link-lazy-missing-one", "link-lazy-missing-two"},
	}}

	grant := cfg.LinkGrantFor(set)
	require.Zero(t, reported,
		"constructing a grant must report nothing: the resolve it wraps is deferred to the first question")

	grant.Granted(bundles.BundleRead{}, "anything")
	require.Equal(t, 2, reported, "the first question resolves once and reports each unloadable bundle ref once")

	grant.Granted(bundles.BundleRead{}, "anything-else")
	assert.Equal(t, 2, reported, "a second question re-uses the resolved set and reports nothing more")
}
