package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// aliasToPersonal is the test alias→URL resolver for the agent-profile migration:
// "personal" is a configured remote, everything else is unknown.
func aliasToPersonal(alias string) string {
	if alias == "personal" {
		return "https://github.com/ben/ctxloom-personal"
	}
	return ""
}

const personalURL = "https://github.com/ben/ctxloom-personal"

// agentProfiles extracts agents.<name>.profiles from a parsed config as a
// []string for assertions.
func agentProfiles(t *testing.T, root map[string]any, name string) []string {
	t.Helper()
	agentsMap, ok := root["agents"].(map[string]any)
	require.True(t, ok, "agents should be a mapping")
	agent, ok := agentsMap[name].(map[string]any)
	require.True(t, ok, "agent %q should be a mapping", name)
	raw, ok := agent["profiles"].([]any)
	require.True(t, ok, "profiles should be a sequence")
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i] = v.(string)
	}
	return out
}

// TestAgentProfileCanonicalizeUpgrade pins the on-load migration (decision B):
// a short "<remote>/<bundle>#profiles/<name>" agent profile is rewritten to its
// canonical URL, while bare/local and canonical refs are left verbatim.
func TestAgentProfileCanonicalizeUpgrade(t *testing.T) {
	pipe := upgrade.Pipeline{agentProfileCanonicalizeUpgrade{aliasToURL: aliasToPersonal}}
	in := "agents:\n" +
		"  dev:\n" +
		"    engine: claude-code\n" +
		"    profiles:\n" +
		"      - personal/agent-ensemble#profiles/finder\n" + // alias → canonicalize
		"      - developer\n" + // bare local → verbatim
		"      - tools#profiles/probe\n" + // local bundle profile → verbatim
		"      - ctxloom:local@bundles/dev#profiles/x\n" + // already canonical → verbatim
		"      - work/agent-ensemble#profiles/finder\n" // unknown alias → verbatim

	out, applied := pipe.Run([]byte(in))
	require.NotEmpty(t, applied, "the short alias ref should trigger a rewrite")

	var root map[string]any
	require.NoError(t, yaml.Unmarshal(out, &root))
	got := agentProfiles(t, root, "dev")
	assert.Equal(t, []string{
		personalURL + "@bundles/agent-ensemble#profiles/finder",
		"developer",
		"tools#profiles/probe",
		"ctxloom:local@bundles/dev#profiles/x",
		"work/agent-ensemble#profiles/finder",
	}, got)

	// Idempotent: a second pass over the already-canonicalized output changes nothing.
	_, again := pipe.Run(out)
	assert.Empty(t, again, "migration must be idempotent")
}

// TestAgentProfileCanonicalizeUpgrade_NilResolver pins the fault-tolerant
// self-gate: with no registry resolver the migration is a no-op and short refs
// survive for the read-path loader to resolve.
func TestAgentProfileCanonicalizeUpgrade_NilResolver(t *testing.T) {
	pipe := upgrade.Pipeline{agentProfileCanonicalizeUpgrade{aliasToURL: nil}}
	in := "agents:\n  dev:\n    profiles:\n      - personal/agent-ensemble#profiles/finder\n"
	out, applied := pipe.Run([]byte(in))
	assert.Empty(t, applied)
	assert.Equal(t, in, string(out))
}
