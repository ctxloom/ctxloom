package operations

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// Moved from internal/cli/acp_cmd_test.go (ISO0 extraction) alongside the
// functions they test: acpSessionMCPServers and sessionModesFrom now live in
// engine_session.go, package operations.

// TestACPSessionMCPServers: an ACP session-open composes the managed MCP set
// from the session's cwd config — ctxloom's own context server plus every
// discovered companion's loadout (S8 — taskloom's `taskloom mcp` server used
// to be an embedded builtin bundle, now its own loadout) — for session/new
// injection, and a client-supplied server of the same name wins over the
// managed entry.
func TestACPSessionMCPServers(t *testing.T) {
	defer config.AdmitEveryDiscoveredCompanionForTesting()()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	// taskloom's loadout is PATH-gated (companion discovery); this Config
	// never calls SetExecutableTrustGate, so ResolveBundleMCPServers runs
	// with a nil gate (no enforcement, matching its management/listing
	// convention) — an unsigned fake loadout is sufficient to exercise the
	// injection wiring itself.
	restore := config.SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "taskloom" {
			return "/fake/taskloom", nil
		}
		return "", exec.ErrNotFound
	})
	defer restore()
	envelope, err := signing.EncodeLoadoutEnvelope(
		[]byte("version: \"1.0.0\"\nmcp:\n  taskloom:\n    command: taskloom\n    args: [\"mcp\"]\n"), nil, "")
	require.NoError(t, err)
	restoreProbe := config.SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	got := acpSessionMCPServers(cfg, "claude-code", nil, nil)

	byName := make(map[string]agent.ChatMCPServer, len(got))
	for _, s := range got {
		byName[s.Name] = s
	}
	require.Contains(t, byName, "ctxloom")
	// Command names the self-exec absolute path (agent.CtxloomCommand), not
	// the bare name — see its doc for the staged-vs-installed invariant.
	assert.Equal(t, agent.CtxloomCommand(), byName["ctxloom"].Command)
	assert.Equal(t, []string{"mcp"}, byName["ctxloom"].Args)
	require.Contains(t, byName, "taskloom")
	assert.Equal(t, "taskloom", byName["taskloom"].Command)
	assert.Equal(t, []string{"mcp"}, byName["taskloom"].Args)

	// Dedup: the client already supplies a "taskloom" server → not injected.
	deduped := acpSessionMCPServers(cfg, "claude-code", nil,
		[]agent.ChatMCPServer{{Name: "taskloom", Command: "/custom/taskloom"}})
	for _, s := range deduped {
		assert.NotEqual(t, "taskloom", s.Name, "client-supplied name must win over the managed entry")
	}
}

// TestSessionModesFrom_ProfilesAndAgents: the mode list is default set,
// then profiles (each assembling just itself), then agents (each assembling
// its composed profile set, carrying its declared engine, namespaced so a
// agent can never collide with a profile of the same name).
func TestSessionModesFrom_ProfilesAndAgents(t *testing.T) {
	subs := []AgentEntry{
		{Name: "reviewer", Engine: "fast", Profiles: []string{"r1", "r2"}},
		{Name: "docs", Profiles: []string{"d"}},
	}
	modes := sessionModesFrom([]string{"go", "reviewer"}, subs, "", []string{"go", "base"}, "")
	require.NotNil(t, modes)

	assert.Equal(t, DefaultModeID, modes.Current)
	require.Len(t, modes.Available, 5)

	assert.Equal(t, DefaultModeID, modes.Available[0].ID)
	assert.Equal(t, "default (go, base)", modes.Available[0].Name)
	assert.Nil(t, modes.Available[0].Profiles, "the default mode assembles the configured defaults")

	assert.Equal(t, "go", modes.Available[1].ID)
	assert.Equal(t, []string{"go"}, modes.Available[1].Profiles)

	// The agent "reviewer" coexists with the PROFILE "reviewer": namespaced id.
	assert.Equal(t, "agent:reviewer", modes.Available[3].ID)
	assert.Equal(t, "reviewer (agent)", modes.Available[3].Name)
	assert.Equal(t, []string{"r1", "r2"}, modes.Available[3].Profiles)
	assert.Equal(t, "fast", modes.Available[3].Engine)

	assert.Equal(t, "agent:docs", modes.Available[4].ID)
	assert.Empty(t, modes.Available[4].Engine)
}

// TestSessionModesFrom_CurrentSelection: the launch selection decides the
// current mode — agent beats profile beats default.
func TestSessionModesFrom_CurrentSelection(t *testing.T) {
	subs := []AgentEntry{{Name: "reviewer", Profiles: []string{"r"}}}

	modes := sessionModesFrom([]string{"go"}, subs, "", nil, "reviewer")
	require.NotNil(t, modes)
	assert.Equal(t, "agent:reviewer", modes.Current)

	modes = sessionModesFrom([]string{"go"}, subs, "go", nil, "")
	require.NotNil(t, modes)
	assert.Equal(t, "go", modes.Current)
}

// TestSessionModesFrom_UnlistedInitialProfileAppended: a launch profile the
// loader does not list (e.g. a bundle profile) still becomes a selectable mode.
func TestSessionModesFrom_UnlistedInitialProfileAppended(t *testing.T) {
	modes := sessionModesFrom([]string{"go"}, nil, "kit#profiles/review", nil, "")
	require.NotNil(t, modes)
	assert.Equal(t, "kit#profiles/review", modes.Current)
	last := modes.Available[len(modes.Available)-1]
	assert.Equal(t, "kit#profiles/review", last.ID)
	assert.Equal(t, []string{"kit#profiles/review"}, last.Profiles)
}

// TestSessionModesFrom_Empty: nothing to advertise → no modes block at all.
func TestSessionModesFrom_Empty(t *testing.T) {
	assert.Nil(t, sessionModesFrom(nil, nil, "", nil, ""))
}

// TestSessionModesFrom_AgentsOnly: agents alone still advertise modes
// (default + agents) even with no installed profiles.
func TestSessionModesFrom_AgentsOnly(t *testing.T) {
	modes := sessionModesFrom(nil, []AgentEntry{{Name: "docs", Profiles: []string{"d"}}}, "", nil, "")
	require.NotNil(t, modes)
	require.Len(t, modes.Available, 2)
	assert.Equal(t, "agent:docs", modes.Available[1].ID)
}
