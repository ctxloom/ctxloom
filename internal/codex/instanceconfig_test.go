package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// realisticHostCodexConfig stands in for a live user's ~/.codex/config.toml: the
// preferences that SHOULD reach every agent session (model, approval_policy,
// sandbox, a named profile) sitting in the same file as the two sections that
// must never leave the host — their own MCP registrations, with a secret in an
// env block, and their own hook commands.
const realisticHostCodexConfig = `model = "o3"
approval_policy = "on-request"
sandbox_mode = "workspace-write"

[profiles.deep]
model = "o3-pro"

[mcp_servers.spotify]
command = "spotify-mcp"
args = ["--token", "SECRET-SPOTIFY-TOKEN"]

[mcp_servers.gmail]
command = "gmail-mcp"

[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "curl https://example.invalid/personal-webhook"

[projects."/home/user/some-other-repo"]
trust_level = "trusted"
`

// writeHostCodexConfig lays a ~/.codex/config.toml into a scratch host home.
func writeHostCodexConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ConfigDirName), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ConfigDirName, ConfigFileName), []byte(body), 0o600))
	return home
}

// readInstanceConfigTOML reads the seeded instance config.toml back as a table.
func readInstanceConfigTOML(t *testing.T, instanceHome string) (map[string]any, string) {
	t.Helper()
	path := filepath.Join(instanceHome, ConfigDirName, ConfigFileName)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the instance config.toml must exist at <CODEX_HOME>/%s", ConfigFileName)
	require.NotEmpty(t, data, "an empty config.toml is this project's signature false green")
	cfg := map[string]any{}
	require.NoError(t, toml.Unmarshal(data, &cfg))
	return cfg, string(data)
}

// TestWriteInstanceConfig_CarriesPreferencesAndElidesTheTwoSections is D5's
// payload test: the user's own model/approval_policy/sandbox/profiles become
// the instance's base, and [mcp_servers] and [hooks] do not cross at all.
//
// MUTATION TARGET (m2): empty elidedHostSections, drop one of its entries, or
// skip the delete loop, and this goes red naming the section that leaked.
func TestWriteInstanceConfig_CarriesPreferencesAndElidesTheTwoSections(t *testing.T) {
	host := writeHostCodexConfig(t, realisticHostCodexConfig)
	instance := t.TempDir()

	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, rep.Wrote, 1)

	cfg, raw := readInstanceConfigTOML(t, instance)

	// Carried — this is what makes copying the file worth doing at all.
	assert.Equal(t, "o3", cfg["model"])
	assert.Equal(t, "on-request", cfg["approval_policy"])
	assert.Equal(t, "workspace-write", cfg["sandbox_mode"])
	profiles, ok := cfg["profiles"].(map[string]any)
	require.True(t, ok, "the user's named profiles must apply to an agent session too")
	assert.Contains(t, profiles, "deep")

	// Elided — each is a live confidentiality or code-execution question.
	assert.NotContains(t, cfg, "mcp_servers",
		"the user's own MCP registrations must never cross into an agent's instance")
	assert.NotContains(t, cfg, "hooks",
		"the user's own hook commands must never run on an agent's schedule")
	assert.NotContains(t, raw, "SECRET-SPOTIFY-TOKEN")
	assert.NotContains(t, raw, "personal-webhook")
}

// TestElidedHostSections_IsExactlyTheRuledPair is the roster guard: the elision
// list is a DECISION (D5), and widening or narrowing it is a confidentiality
// decision rather than a refactor.
func TestElidedHostSections_IsExactlyTheRuledPair(t *testing.T) {
	assert.Equal(t, []string{"mcp_servers", "hooks"}, elidedHostSections,
		"the elision list changed. Everything NOT listed here crosses from the user's real ~/.codex into every agent's instance")
}

// TestWriteInstanceConfig_SeedsOnce: a destination that already exists is left
// alone. By the second run it carries ctxloom's managed sections and this
// session's trust pre-seed, and re-seeding from the host would drop them for
// however long it takes the delivery path to put them back — a base established
// twice is not more correct, only more destructive.
func TestWriteInstanceConfig_SeedsOnce(t *testing.T) {
	host := writeHostCodexConfig(t, realisticHostCodexConfig)
	instance := t.TempDir()
	w := NewInstanceConfigWriter(agent.SettingsOptions{})
	req := agent.InstanceConfigRequest{HostHome: host, InstanceHome: instance, WorkDir: t.TempDir()}

	_, err := w.WriteInstanceConfig(req)
	require.NoError(t, err)

	// Stand in for the managed sections the delivery path writes on top.
	path := filepath.Join(instance, ConfigDirName, ConfigFileName)
	existing, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(existing, []byte("\n[mcp_servers.ctxloom]\ncommand = \"ctxloom\"\n")...), 0o600))

	rep, err := w.WriteInstanceConfig(req)
	require.NoError(t, err)
	assert.Empty(t, rep.Wrote, "a second call must not re-seed over the delivered managed sections")

	cfg, _ := readInstanceConfigTOML(t, instance)
	servers, ok := cfg["mcp_servers"].(map[string]any)
	require.True(t, ok, "the managed section written on top must survive")
	assert.Contains(t, servers, "ctxloom")
	assert.NotContains(t, servers, "spotify", "and re-seeding must not have brought the host's own back")
}

// TestWriteInstanceConfig_NoHostConfigWritesNothing: a user who never wrote a
// config.toml contributes nothing — and critically, no zero-byte file. A
// success report over an empty file is this project's characteristic bug.
func TestWriteInstanceConfig_NoHostConfigWritesNothing(t *testing.T) {
	instance := t.TempDir()
	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: t.TempDir(), InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Empty(t, rep.Wrote)
	assert.NoFileExists(t, filepath.Join(instance, ConfigDirName, ConfigFileName))
}

// TestWriteInstanceConfig_WhollyElidedHostConfigWritesNothing: a host file that
// held ONLY the two elided sections leaves nothing to carry. Writing the empty
// remainder would produce a zero-byte config.toml reported as a successful
// write.
func TestWriteInstanceConfig_WhollyElidedHostConfigWritesNothing(t *testing.T) {
	host := writeHostCodexConfig(t, "[mcp_servers.spotify]\ncommand = \"spotify-mcp\"\n")
	instance := t.TempDir()

	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Empty(t, rep.Wrote)
	assert.NoFileExists(t, filepath.Join(instance, ConfigDirName, ConfigFileName))
}

// TestWriteInstanceConfig_UnparseableHostConfigWarnsAndProceeds: the user's
// file is theirs; ctxloom neither owns nor repairs it. A parse failure costs
// their preferences and says so, and never blocks the run.
func TestWriteInstanceConfig_UnparseableHostConfigWarnsAndProceeds(t *testing.T) {
	host := writeHostCodexConfig(t, "model = \"o3\"\n[[[broken\n")
	instance := t.TempDir()

	rep, err := NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: instance, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, rep.Warnings, 1)
	assert.Contains(t, rep.Warnings[0], "cannot parse")
	assert.Empty(t, rep.Wrote)
}

// TestWriteInstanceConfig_NeverWritesTheHostHome: the ambient source is READ.
// tests/arch's byte-identity gate is the whole-tree version of this; here it is
// pinned at the one function that touches the file.
func TestWriteInstanceConfig_NeverWritesTheHostHome(t *testing.T) {
	host := writeHostCodexConfig(t, realisticHostCodexConfig)
	hostFile := filepath.Join(host, ConfigDirName, ConfigFileName)
	before, err := os.ReadFile(hostFile)
	require.NoError(t, err)

	_, err = NewInstanceConfigWriter(agent.SettingsOptions{}).WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome: host, InstanceHome: t.TempDir(), WorkDir: t.TempDir(),
	})
	require.NoError(t, err)

	after, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the user's own ~/.codex/config.toml was modified")

	entries, err := os.ReadDir(filepath.Join(host, ConfigDirName))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "seeding added files to the user's own ~/.codex")
}
