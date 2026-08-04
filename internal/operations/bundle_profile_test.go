package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// writeBundleProfileFixture writes a local bundle that ships a fragment, an MCP
// server, a hook, AND a profile that composes them — exercising the new ungated,
// compound `profiles:` bundle item kind end to end. Real tempdirs (not
// afero.MemMapFs) because GetBundleDirs/GetProfileDirs stat the real fs.
//
//	<root>/.ctxloom/content/bundles/kit.yaml
//
// The bundle profile "p1" is addressable as
// "ctxloom:local@bundles/kit#profiles/p1".
func writeBundleProfileFixture(t *testing.T, root string) {
	t.Helper()
	bundleDir := filepath.Join(root, ".ctxloom", "content", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))

	kitYAML := `version: "1.0.0"
fragments:
  f1:
    content: "FRAG-ONE"
mcp:
  db:
    command: echo
    args: ["db"]
hooks:
  post_file_edit:
    - command: "echo edited"
profiles:
  p1:
    description: "kit profile"
    llm: fast
    bundles:
      - ctxloom:local@bundles/kit
`
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "kit.yaml"), []byte(kitYAML), 0644))
}

func bundleProfileConfig(root string) *config.Config {
	return config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join(root, ".ctxloom")}})
}

const kitProfileKey = "ctxloom:local@bundles/kit#profiles/p1"

// TestBundleProfile_ResolvesThroughProfileLoader proves a bundle profile
// resolves into a runnable profile through the SAME path run/agent_run use
// (GetProfileLoader().ResolveProfile), carrying its composed bundles and its
// llm (engine) preference.
func TestBundleProfile_ResolvesThroughProfileLoader(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root)
	cfg := bundleProfileConfig(root)

	resolved, err := cfg.GetProfileLoader().ResolveProfile(kitProfileKey, nil)
	require.NoError(t, err)
	assert.Equal(t, "fast", resolved.LLM, "bundle profile's llm (engine selection) resolves")
	assert.Contains(t, resolved.Bundles, "ctxloom:local@bundles/kit",
		"bundle profile composes its referenced bundles")
}

// TestBundleProfile_AssemblesContent proves the composed fragment actually flows
// into assembled context when the bundle profile is the active profile — i.e. it
// runs exactly like a local/inline profile.
func TestBundleProfile_AssemblesContent(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root)
	cfg := bundleProfileConfig(root)

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: kitProfileKey})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, []string{kitProfileKey}, res.Profiles)
	assert.Contains(t, res.Context, "FRAG-ONE",
		"the profile's composed fragment must reach assembled context")
}

// TestBundleProfile_ListAndShowAttribution proves `profile list` / `profile show`
// surface a bundle-sourced profile, attributed to its bundle and labeled with a
// friendly display name.
func TestBundleProfile_ListAndShowAttribution(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root)
	cfg := bundleProfileConfig(root)
	ctx := context.Background()

	list, err := ListProfiles(ctx, cfg, ListProfilesRequest{})
	require.NoError(t, err)

	var entry *ProfileEntry
	for i := range list.Profiles {
		if list.Profiles[i].Name == kitProfileKey {
			entry = &list.Profiles[i]
		}
	}
	require.NotNil(t, entry, "bundle profile must appear in `profile list`")
	assert.Equal(t, "ctxloom:local@bundles/kit", entry.Bundle, "attributed to its bundle")
	assert.Equal(t, "p1", entry.DisplayName, "friendly display name is the bare profile name")
	assert.True(t, entry.IsRemote, "bundle profiles are seeded (read-only) references")

	show, err := GetProfile(ctx, cfg, GetProfileRequest{Name: kitProfileKey})
	require.NoError(t, err)
	assert.Equal(t, "ctxloom:local@bundles/kit", show.Bundle)
	assert.Equal(t, "fast", show.LLM)
}

// TestBundleProfile_MCPStillGatesAtExecChoke is the exec-side half of the
// gate-exemption invariant: the profile DEFINITION is ungated, but an MCP server
// the profile pulls in still passes the executable trust gate — a deny gate
// withholds it.
func TestBundleProfile_MCPStillGatesAtExecChoke(t *testing.T) {
	root := t.TempDir()
	writeBundleProfileFixture(t, root)
	cfg := bundleProfileConfig(root)

	// Ungated: the profile's bundle MCP server is present.
	servers := cfg.ResolveBundleMCPServers([]string{kitProfileKey})
	_, ok := servers["db"]
	assert.True(t, ok, "the profile's MCP server resolves when ungated")

	// Deny exec gate: the same MCP server is withheld at the exec choke.
	cfg.SetExecutableTrustGate(testFilter(false))
	servers = cfg.ResolveBundleMCPServers([]string{kitProfileKey})
	_, ok = servers["db"]
	assert.False(t, ok, "the profile's MCP server still gates at the exec choke")
}

// TestBundleProfile_NotAReviewableKind is the structural gate-exemption
// invariant: the trust ref grammar addresses fragment/prompt/mcp/hook but
// NEVER profiles, so a profile definition can carry no review state and is
// never run through EffectiveTrust (its constituent items are). (This replaces
// the retired TR6-baseline enumeration proxy with the grammar itself.)
func TestBundleProfile_NotAReviewableKind(t *testing.T) {
	_, _, _, err := trust.ParseItemRef("kit#profiles/dev")
	assert.Error(t, err, "a profile must not be addressable as a trust item kind")
}
