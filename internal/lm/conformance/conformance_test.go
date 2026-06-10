//go:build conformance

// The cross-agent equity suite. Gated behind the `conformance` build tag (see
// doc.go) and kept in its own package so it composes claude/antigravity/codex without
// touching their per-module test files — safe alongside concurrent work. Every
// assertion goes through the public agent.SettingsWriter interface, so it is
// format-agnostic (claude JSON, antigravity JSON, codex TOML all pass the same suite).
package conformance

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/antigravity"
	"github.com/ctxloom/claude"
	"github.com/ctxloom/codex"
	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

// agentCase is one agent under test: its writer constructor plus a valid
// settings file carrying a user-authored entry the writer must preserve.
type agentCase struct {
	name       string
	newWriter  func(agent.SettingsOptions) agent.SettingsWriter
	userFile   string // a valid settings file (in the agent's format) with a user entry
	userMarker string // substring of userFile that must survive write + remove
}

// agentCases returns every supported agent. Add a new agent here and it inherits
// the whole equity suite.
func agentCases() []agentCase {
	return []agentCase{
		{"claude-code", claude.NewWriter, `{"theme":"dark"}`, "dark"},
		{"antigravity", antigravity.NewWriter, `{"theme":"dark"}`, "dark"},
		{"codex", codex.NewWriter, "model = \"o3\"\n", "o3"},
	}
}

// coveredEvents are the unified hook events every agent must emit. Each maps to a
// command carrying a unique, greppable suffix so a format-agnostic test can prove
// the event reached the settings file regardless of the agent's event naming.
var coveredEvents = []string{
	"conf-sessionstart", "conf-pretool", "conf-posttool", "conf-preshell", "conf-postfileedit",
}

// standardHooks gives each unified event a command whose executable token is
// `ctxloom` (so every writer recognizes it as managed for removal) plus a unique
// suffix (so coverage can be asserted). SessionEnd is intentionally omitted —
// codex's CLI has no such event.
func standardHooks() *wire.HooksConfig {
	mk := func(suffix string) []wire.Hook {
		return []wire.Hook{{Command: "ctxloom hook " + suffix}}
	}
	return &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: mk("conf-sessionstart"),
		PreTool:      mk("conf-pretool"),
		PostTool:     mk("conf-posttool"),
		PreShell:     mk("conf-preshell"),
		PostFileEdit: mk("conf-postfileedit"),
	}}
}

const projectDir = "/project"

// TestConformance_FaultTolerantLoad: a corrupt existing settings file must not
// block hook application — the writer warns and continues, never erroring.
func TestConformance_FaultTolerantLoad(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			require.NoError(t, afero.WriteFile(fs, w.SettingsPath(projectDir), []byte("!!! not valid !!!"), 0644))

			require.NoError(t, w.WriteSettings(standardHooks(), nil, nil, projectDir),
				"WriteSettings must not error on a corrupt prior file")

			st, err := w.Status(projectDir)
			require.NoError(t, err)
			assert.True(t, st.HooksPresent, "hooks applied despite the corrupt prior file")
		})
	}
}

// TestConformance_AtomicWriteBackup: overwriting an existing settings file leaves
// a .ctxloom.bak of the prior content (crash-safety).
func TestConformance_AtomicWriteBackup(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			path := w.SettingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(a.userFile), 0644))

			require.NoError(t, w.WriteSettings(standardHooks(), nil, nil, projectDir))

			bak, err := afero.ReadFile(fs, path+".ctxloom.bak")
			require.NoError(t, err, "a .ctxloom.bak of the prior settings must exist")
			assert.Contains(t, string(bak), a.userMarker, "backup holds the pre-write content")
		})
	}
}

// TestConformance_HookEventCoverage: every unified hook event must reach the
// settings file. (This is the assertion that catches a missing per-event mapping
// like an agent's absent PreShell/PostFileEdit mapping.)
func TestConformance_HookEventCoverage(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			require.NoError(t, w.WriteSettings(standardHooks(), nil, nil, projectDir))

			data, err := afero.ReadFile(fs, w.SettingsPath(projectDir))
			require.NoError(t, err)
			for _, ev := range coveredEvents {
				assert.Containsf(t, string(data), ev, "unified event %q must be emitted", ev)
			}
		})
	}
}

// TestConformance_MCPAutoRegister: ctxloom's own MCP server is auto-registered
// and reported by Status, with no explicit MCP config.
func TestConformance_MCPAutoRegister(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			require.NoError(t, w.WriteSettings(standardHooks(), nil, nil, projectDir))

			st, err := w.Status(projectDir)
			require.NoError(t, err)
			assert.True(t, st.MCPPresent, "ctxloom MCP server auto-registered")
		})
	}
}

// TestConformance_RemovePreservesUser: RemoveSettings strips every managed
// artifact (Status no longer Wired) while preserving the user's own settings.
func TestConformance_RemovePreservesUser(t *testing.T) {
	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			path := w.SettingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(a.userFile), 0644))

			require.NoError(t, w.WriteSettings(standardHooks(), nil, nil, projectDir))
			require.NoError(t, w.RemoveSettings(projectDir))

			st, err := w.Status(projectDir)
			require.NoError(t, err)
			assert.False(t, st.Wired(), "no managed artifacts remain after removal")

			data, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			assert.Contains(t, string(data), a.userMarker, "user settings preserved through write + remove")
		})
	}
}
