package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// realisticHostClaudeJSON stands in for a live user's ~/.claude.json. On a real
// host that file is NOT a narrow onboarding record — it is claude's whole
// top-level config, carrying the user's own mcpServers registrations (and the
// secrets in their env blocks), their oauthAccount identity, their accumulated
// per-project history and their telemetry keys. Every one of those is present
// here on purpose: a fixture that omitted them could not tell a working
// allow-list from a broken one.
const realisticHostClaudeJSON = `{
  "hasCompletedOnboarding": true,
  "lastOnboardingVersion": "2.1.228",
  "hasIdeOnboardingBeenShown": true,
  "bypassPermissionsModeAccepted": true,
  "autoUpdates": true,
  "oauthAccount": {"emailAddress": "user@example.com", "accountUuid": "abc-123"},
  "userID": "telemetry-user-id",
  "firstStartTime": "2026-01-01T00:00:00Z",
  "mcpServers": {
    "spotify": {"command": "spotify-mcp", "args": ["--token", "SECRET-SPOTIFY-TOKEN"]},
    "gmail": {"command": "gmail-mcp", "env": {"GMAIL_REFRESH_TOKEN": "SECRET-GMAIL-TOKEN"}}
  },
  "projects": {
    "/home/user/some-other-repo": {"hasTrustDialogAccepted": true, "history": ["a private prompt"]}
  }
}`

// writeHostConfig lays realisticHostClaudeJSON (or a caller-supplied variant)
// into a scratch host home and returns that home.
func writeHostConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, InstanceConfigFileName), []byte(body), 0o600))
	return home
}

// readInstanceConfig reads back the generated instance file as a table.
func readInstanceConfig(t *testing.T, instanceHome string) map[string]any {
	t.Helper()
	path := filepath.Join(instanceHome, inTreeConfigLeaf, InstanceConfigFileName)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the instance config must exist at <CLAUDE_CONFIG_DIR>/%s", InstanceConfigFileName)
	require.NotEmpty(t, data, "an empty instance config is this project's signature false green")
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

// TestWriteInstanceConfig_CopiesOnlyTheOnboardingAllowList is the PAYLOAD test
// for D4: the generated instance file carries the onboarding answers by name
// and NOT ONE of the confidential keys sitting beside them in the same host
// file.
//
// MUTATION TARGET (m1): add "mcpServers" to ambientConfigKeys — or replace the
// per-key loop with a wholesale copy of the host table — and the mcpServers
// assertion below goes red naming the leaked key.
func TestWriteInstanceConfig_CopiesOnlyTheOnboardingAllowList(t *testing.T) {
	host := writeHostConfig(t, realisticHostClaudeJSON)
	instance := t.TempDir()
	workDir := t.TempDir()

	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: workDir,
	})
	require.NoError(t, err)
	require.Len(t, rep.Wrote, 1)
	assert.Empty(t, rep.Warnings, "a complete host file is not schema drift")

	cfg := readInstanceConfig(t, instance)

	// Copied, by name.
	assert.Equal(t, true, cfg["hasCompletedOnboarding"])
	assert.Equal(t, "2.1.228", cfg["lastOnboardingVersion"])
	assert.Equal(t, true, cfg["hasIdeOnboardingBeenShown"])

	// NEVER copied. Each of these is a live confidentiality question, not a
	// tidiness preference.
	for _, forbidden := range []string{"mcpServers", "oauthAccount", "userID", "firstStartTime"} {
		assert.NotContains(t, cfg, forbidden,
			"%q is the user's own data and must never cross into an agent's instance", forbidden)
	}

	// And nothing leaked through the raw bytes either — a key could be absent
	// while its VALUE rode in under some other name.
	raw, err := os.ReadFile(filepath.Join(instance, inTreeConfigLeaf, InstanceConfigFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "SECRET-SPOTIFY-TOKEN")
	assert.NotContains(t, string(raw), "SECRET-GMAIL-TOKEN")
	assert.NotContains(t, string(raw), "user@example.com")
	assert.NotContains(t, string(raw), "a private prompt")
}

// TestWriteInstanceConfig_HardensBypassAndAutoUpdate: the two policy keys are
// written FALSE even though the host file says true for both. They are
// ctxloom's policy for a home it created, not a user preference to carry over —
// an agent run must not inherit a standing "skip every permission prompt", and
// a disposable instance must not self-update out from under the probed binary.
func TestWriteInstanceConfig_HardensBypassAndAutoUpdate(t *testing.T) {
	host := writeHostConfig(t, realisticHostClaudeJSON)
	instance := t.TempDir()

	_, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)

	cfg := readInstanceConfig(t, instance)
	assert.Equal(t, false, cfg["bypassPermissionsModeAccepted"], "the host said true; the instance must not inherit it")
	assert.Equal(t, false, cfg["autoUpdates"], "the host said true; a throwaway instance must not self-update")
}

// TestWriteInstanceConfig_TrustIsGeneratedForTheWorkDir pins the half of D4
// that is GENERATED rather than copied: the workspace-trust answer is keyed to
// the directory the run works in, and the host's own projects map does not come
// along.
//
// MUTATION TARGET (m3): key the entry to req.InstanceHome (or to the claude
// config dir) instead of req.WorkDir and this goes red — trusting the config
// home answers a question claude never asks while leaving the real workspace
// untrusted, which headless means proceeding without tools rather than
// prompting.
func TestWriteInstanceConfig_TrustIsGeneratedForTheWorkDir(t *testing.T) {
	host := writeHostConfig(t, realisticHostClaudeJSON)
	instance := t.TempDir()
	workDir := t.TempDir()

	_, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: workDir,
	})
	require.NoError(t, err)

	cfg := readInstanceConfig(t, instance)
	projects, ok := cfg["projects"].(map[string]any)
	require.True(t, ok, "the generated trust entry must live under projects")

	entry, ok := projects[workDir].(map[string]any)
	require.True(t, ok, "trust must be keyed to the WORKING directory %q, got keys %v", workDir, keysOf(projects))
	assert.Equal(t, true, entry["hasTrustDialogAccepted"])
	assert.Equal(t, true, entry["hasCompletedProjectOnboarding"])
	assert.Equal(t, float64(1), entry["projectOnboardingSeenCount"])

	assert.NotContains(t, projects, instance, "the instance home is not a workspace and must never be trusted as one")
	assert.NotContains(t, projects, filepath.Join(instance, inTreeConfigLeaf))
	assert.NotContains(t, projects, "/home/user/some-other-repo",
		"the host's own projects map carries their history and must never be copied wholesale")
	assert.Len(t, projects, 1, "exactly one project — the one this run works in")
}

// TestWriteInstanceConfig_WarnsWhenTheHostDropsAnExpectedKey is the schema-drift
// guard. A host file that no longer carries hasCompletedOnboarding means claude
// renamed or retired the key: the allow-list is now copying nothing while still
// reporting success, which is exactly the silent behaviour change fail-loud
// exists to catch.
func TestWriteInstanceConfig_WarnsWhenTheHostDropsAnExpectedKey(t *testing.T) {
	host := writeHostConfig(t, `{"lastOnboardingVersion": "2.1.228"}`)
	instance := t.TempDir()

	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, rep.Warnings, 1, "exactly the one absent expected key warns, got %v", rep.Warnings)
	assert.Contains(t, rep.Warnings[0], "hasCompletedOnboarding")
	assert.Contains(t, rep.Warnings[0], "schema may have changed")

	cfg := readInstanceConfig(t, instance)
	assert.Equal(t, true, cfg["hasCompletedOnboarding"], "the declared fallback still applies — the run is not blocked on drift")
	assert.Equal(t, "2.1.228", cfg["lastOnboardingVersion"], "the key that IS present still crosses")
}

// TestWriteInstanceConfig_AbsentHostFileStillProducesAUsableInstance: a host
// with no ~/.claude.json at all (a fresh machine) is not a failure. The
// fallbacks and the generated trust entry are written, and the drift guard says
// what could not be carried.
func TestWriteInstanceConfig_AbsentHostFileStillProducesAUsableInstance(t *testing.T) {
	instance := t.TempDir()
	workDir := t.TempDir()

	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: t.TempDir(), InstanceHome: instance, WorkDir: workDir,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, rep.Warnings)

	cfg := readInstanceConfig(t, instance)
	assert.Equal(t, true, cfg["hasCompletedOnboarding"])
	assert.NotContains(t, cfg, "lastOnboardingVersion", "no host value and no invented one — claude writes its own")
	projects := cfg["projects"].(map[string]any)
	assert.Contains(t, projects, workDir)
}

// TestWriteInstanceConfig_NeverWritesTheHostHome: the ambient source is READ.
// The whole per-session instance model rests on the real home staying the
// durable truth, so this is asserted on the bytes, not on the intent.
//
// MUTATION TARGET (m4, local half): make any step write back into req.HostHome
// and this goes red here as well as in tests/arch's byte-identity gate.
func TestWriteInstanceConfig_NeverWritesTheHostHome(t *testing.T) {
	host := writeHostConfig(t, realisticHostClaudeJSON)
	hostFile := filepath.Join(host, InstanceConfigFileName)
	before, err := os.ReadFile(hostFile)
	require.NoError(t, err)

	_, err = NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: t.TempDir(), WorkDir: t.TempDir(),
	})
	require.NoError(t, err)

	after, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the user's own ~/.claude.json was modified")

	entries, err := os.ReadDir(host)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "generation added files to the user's own home")
}

// TestWriteInstanceConfig_OwnerOnly: the instance file records this run's trust
// answer and sits beside live credential bytes; it is owner-only, never
// world-readable.
func TestWriteInstanceConfig_OwnerOnly(t *testing.T) {
	instance := t.TempDir()
	_, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: writeHostConfig(t, realisticHostClaudeJSON), InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(instance, inTreeConfigLeaf, InstanceConfigFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestWriteInstanceConfig_SecondRunPreservesWhatClaudeWrote: two runs share ONE
// session instance (a coordinator and its in-tree delegated child inherit the
// harp), and claude writes into this file while it runs. The second generation
// refreshes the three ctxloom-owned classes and leaves everything else — a
// reset here would wipe live state out from under a running engine.
func TestWriteInstanceConfig_SecondRunPreservesWhatClaudeWrote(t *testing.T) {
	host := writeHostConfig(t, realisticHostClaudeJSON)
	instance := t.TempDir()
	workDir := t.TempDir()
	w := NewInstanceConfigWriter(agent.SettingsOptions{})
	req := agent.InstanceConfigRequest{HostHome: host, InstanceHome: instance, WorkDir: workDir}

	_, err := w.WriteInstanceConfig(req)
	require.NoError(t, err)

	// Stand in for whatever claude accumulated during the first run.
	path := filepath.Join(instance, inTreeConfigLeaf, InstanceConfigFileName)
	cfg := readInstanceConfig(t, instance)
	cfg["numStartups"] = float64(7)
	cfg["projects"].(map[string]any)[workDir].(map[string]any)["history"] = []any{"the child's own prompt"}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	_, err = w.WriteInstanceConfig(req)
	require.NoError(t, err)

	after := readInstanceConfig(t, instance)
	assert.Equal(t, float64(7), after["numStartups"], "the engine's own instance state must survive a second run's generation")
	entry := after["projects"].(map[string]any)[workDir].(map[string]any)
	assert.Equal(t, []any{"the child's own prompt"}, entry["history"], "other keys of this project's entry survive")
	assert.Equal(t, true, entry["hasTrustDialogAccepted"], "and the generated answer is still there")
}

// TestWriteInstanceConfig_ReportsAPrecedenceFileShadowingIt: claude prefers a
// .config.json in the same directory. ctxloom never writes one, but if a user
// does, everything generated here is inert — a silent no-op wearing a success's
// clothes, so it is named.
func TestWriteInstanceConfig_ReportsAPrecedenceFileShadowingIt(t *testing.T) {
	instance := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(instance, inTreeConfigLeaf), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(instance, inTreeConfigLeaf, precedenceConfigFileName), []byte(`{}`), 0o600))

	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: writeHostConfig(t, realisticHostClaudeJSON), InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.NotEmpty(t, rep.Warnings)
	assert.True(t, strings.Contains(strings.Join(rep.Warnings, "\n"), precedenceConfigFileName),
		"a shadowing %s must be reported, got %v", precedenceConfigFileName, rep.Warnings)
}

// TestAmbientConfigKeys_IsAnAllowListOfOnboardingAnswersOnly is the roster
// guard: the allow-list is small, named, and contains nothing that could carry
// user data. It fails the moment someone adds a key whose value is not an
// onboarding answer.
func TestAmbientConfigKeys_IsAnAllowListOfOnboardingAnswersOnly(t *testing.T) {
	want := []string{
		"hasCompletedOnboarding",
		"lastOnboardingVersion",
		"hasIdeOnboardingBeenShown",
		"hasClaudeMdExternalIncludesApproved",
		"hasClaudeMdExternalIncludesWarningShown",
	}
	var got []string
	for _, k := range ambientConfigKeys {
		got = append(got, k.name)
	}
	assert.Equal(t, want, got,
		"the ambient allow-list changed. Every entry crosses from the user's real home into every agent's instance — adding one is a confidentiality decision, not a refactor")
}

// keysOf renders a table's keys for a failure message.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
