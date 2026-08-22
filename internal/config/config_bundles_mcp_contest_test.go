package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// mcpContestFixture lays down two profiles, each naming one local bundle, and
// returns a Config whose appPaths point at the tree. bundleYAML is keyed by
// bundle name; profileBundles maps profile name to the bundle it references.
func mcpContestFixture(t *testing.T, bundleYAML map[string]string, profileBundles map[string]string) *Config {
	t.Helper()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPath(appDir) // committed content tree
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))

	for name, body := range bundleYAML {
		require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, name+".yaml"), []byte(body), 0o644))
	}
	for profile, bundle := range profileBundles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, profile+".yaml"),
			[]byte("name: "+profile+"\nbundles:\n  - "+bundle+"\n"), 0o644))
	}

	return &Config{
		defaultAgent: "default",
		agents:       map[string]agents.Agent{"default": {Profiles: []string{}}},
		appPaths:     []string{appDir},
	}
}

func mcpBundleYAML(servers ...[2]string) string {
	out := "version: \"1.0\"\nmcp:\n"
	for _, s := range servers {
		out += "  " + s[0] + ":\n    command: " + s[1] + "\n"
	}
	return out
}

// TestResolveBundleMCPServers_ContestedName_IsWithheldLoudly is the REFUSAL
// test, and the load-bearing one for the 2026-08-17 ruling.
//
// Before the fix, addServers did a bare `result[name] = server`, so the LAST
// source to declare a name silently won: one bundle's server answered for
// another bundle's declared name, with nothing logged, nothing failed, and the
// resolved map — keyed by name — carrying no trace that a substitution
// happened at all. That is the delivery-substitution path the ruling closes.
//
// The ruling: two DIFFERENT source refs claiming one name is a LOUD error —
// a strictness.ClassBundle finding naming both refs and the contested name —
// and the later claim is WITHHELD, so the incumbent's server is what the
// session actually gets.
func TestResolveBundleMCPServers_ContestedName_IsWithheldLoudly(t *testing.T) {
	resetStrictness(t)

	cfg := mcpContestFixture(t,
		map[string]string{
			"bundle-a": mcpBundleYAML([2]string{"shared-server", "alpha-cmd"}, [2]string{"alpha-only", "a-cmd"}),
			"bundle-b": mcpBundleYAML([2]string{"shared-server", "beta-cmd"}, [2]string{"beta-only", "b-cmd"}),
		},
		map[string]string{"alpha": "bundle-a", "beta": "bundle-b"},
	)

	mark := strictness.Checkpoint()
	result := cfg.ResolveBundleMCPServers([]string{"alpha", "beta"})

	// EFFECT, not exit code: the contender's command must not be what the
	// session would launch. This is the assertion the old code failed.
	require.Contains(t, result, "shared-server")
	assert.Equal(t, "alpha-cmd", result["shared-server"].Command,
		"the incumbent's server survives; the contender must NOT silently substitute for it")
	assert.Contains(t, result["shared-server"].SCM, "bundle-a",
		"provenance follows the surviving server, so reconciliation attributes it to the bundle that actually shipped it")

	// One contest must not block the rest of the resolve.
	assert.Contains(t, result, "alpha-only", "an uncontested server from the incumbent still resolves")
	assert.Contains(t, result, "beta-only", "an uncontested server from the refused bundle still resolves")

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "a withheld MCP server must be reported, not silently dropped")
	assert.Equal(t, strictness.ClassBundle, findings[0].Class)
	assert.Contains(t, findings[0].Message, "shared-server", "the finding names the contested server")
	assert.Contains(t, findings[0].Message, "bundle-a", "the finding names the ref that claimed it first")
	assert.Contains(t, findings[0].Message, "bundle-b", "the finding names the ref that was refused")
	assert.NotEmpty(t, findings[0].FixIt, "a fatal finding must carry the edit that resolves it")
}

// TestResolveBundleMCPServers_SameRefTwice_DedupesSilently is the other half of
// the ruling, and the guard against over-firing: one bundle reached through two
// profiles in scope is the SAME source ref, not a contest. It must resolve
// normally and record NOTHING — a rule that fired here would make every shared
// bundle a fatal startup finding.
func TestResolveBundleMCPServers_SameRefTwice_DedupesSilently(t *testing.T) {
	resetStrictness(t)

	cfg := mcpContestFixture(t,
		map[string]string{"shared-bundle": mcpBundleYAML([2]string{"dup-server", "dup-cmd"})},
		map[string]string{"alpha": "shared-bundle", "beta": "shared-bundle"},
	)

	mark := strictness.Checkpoint()
	result := cfg.ResolveBundleMCPServers([]string{"alpha", "beta"})

	require.Contains(t, result, "dup-server", "the same ref reached twice still contributes its server")
	assert.Equal(t, "dup-cmd", result["dup-server"].Command)
	assert.Empty(t, strictness.Since(mark),
		"the same source ref reached through two profiles dedupes silently and is not a contest")
}

// TestResolveBundleMCPServers_ContestSurvivesTheIncumbent pins which ref the
// ledger keeps after a refusal: the FIRST claimant, not the one just refused.
// If a refusal overwrote the holder, a third source contesting the same name
// would be told it lost to the loser, and two bundles could alternate the name
// between them with each pass reporting a different, wrong pair of refs.
func TestResolveBundleMCPServers_ContestSurvivesTheIncumbent(t *testing.T) {
	resetStrictness(t)

	cfg := mcpContestFixture(t,
		map[string]string{
			"bundle-a": mcpBundleYAML([2]string{"shared-server", "alpha-cmd"}),
			"bundle-b": mcpBundleYAML([2]string{"shared-server", "beta-cmd"}),
			"bundle-c": mcpBundleYAML([2]string{"shared-server", "gamma-cmd"}),
		},
		map[string]string{"alpha": "bundle-a", "beta": "bundle-b", "gamma": "bundle-c"},
	)

	mark := strictness.Checkpoint()
	result := cfg.ResolveBundleMCPServers([]string{"alpha", "beta", "gamma"})

	assert.Equal(t, "alpha-cmd", result["shared-server"].Command,
		"the first claimant holds the name against every later contender")

	findings := strictness.Since(mark)
	require.Len(t, findings, 2, "each refused contender is reported")
	for _, f := range findings {
		assert.Contains(t, f.Message, "bundle-a",
			"every contest names the ORIGINAL holder, never the previously-refused contender")
	}
	assert.Contains(t, findings[0].Message, "bundle-b")
	assert.Contains(t, findings[1].Message, "bundle-c")
}
