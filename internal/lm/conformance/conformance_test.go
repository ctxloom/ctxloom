//go:build conformance

// The cross-agent equity suite. Gated behind the `conformance` build tag (see
// doc.go) and kept in its own package so it composes claude/antigravity/codex without
// touching their per-module test files — safe alongside concurrent work. Every
// assertion goes through the public agent.SettingsWriter interface, so it is
// format-agnostic (claude JSON, antigravity JSON, codex TOML all pass the same suite).
package conformance

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// settingsWriter is the suite's view of an agent writer: the shared
// agent.SettingsWriter contract plus the concrete writers' SettingsPath method
// (no longer part of the interface; every concrete writer still exposes it).
type settingsWriter interface {
	agent.SettingsWriter
	SettingsPath(projectDir string) string
}

// agentCase is one agent under test: its writer constructor plus a valid
// settings file carrying a user-authored entry the writer must preserve.
type agentCase struct {
	name       string
	newWriter  func(agent.SettingsOptions) settingsWriter
	userFile   string // a valid settings file (in the agent's format) with a user entry
	userMarker string // substring of userFile that must survive write + remove
}

// agentCases returns the THREE agents this suite actually covers today
// (claude-code, antigravity, codex) — NOT every agent.SettingsWriter
// implementation in the repo (U058-F03): opencode and kiro both implement
// the interface (internal/opencode/settings.go:465,
// internal/kiro/settings.go:48) and are absent, for two DIFFERENT reasons,
// not one shared oversight:
//
//   - kiro's writer has no SettingsPath method at all — its settings are
//     genuinely multi-file (agentPath/mcpPath/mcpLedgerPath/steeringPath,
//     see kiro/settings.go), so it does not implement this suite's
//     settingsWriter interface and `concrete[*kiro.KiroWriter]` does not
//     COMPILE (see concrete's doc). Covering kiro needs this suite's
//     path-based assertions (WriteSettings/AtomicWriteBackup/
//     RemovePreservesUser all read/write ONE file at SettingsPath) redesigned
//     for a multi-file writer first — not a one-line table addition.
//   - opencode's WriteSettings explicitly IGNORES the hooks argument (it "has
//     no ctxloom-style hook mechanism", see opencode/settings.go's own doc
//     comment) — TestConformance_HookEventCoverage would fail immediately,
//     correctly, on a documented and deliberate design gap this suite has no
//     vocabulary to distinguish from a real regression (the way the codex
//     no-backup case above IS distinguished, via backsUpCorruptFile).
//
// Add a new agent here ONLY once it can actually honor every assertion below
// (or once each assertion gains the same per-agent escape hatch
// backsUpCorruptFile already models for the one divergence that already had
// one) — a new agent here inherits the WHOLE equity suite unconditionally.
func agentCases() []agentCase {
	return []agentCase{
		{"claude-code", concrete[*claude.ClaudeCodeHookWriter](claude.NewWriter), `{"theme":"dark"}`, "dark"},
		{"antigravity", concrete[*antigravity.AntigravityHookWriter](antigravity.NewWriter), `{"theme":"dark"}`, "dark"},
		{"codex", concrete[*codex.CodexHookWriter](codex.NewWriter), "model = \"o3\"\n", "o3"},
	}
}

// concrete widens a constructor's agent.SettingsWriter result to this suite's
// richer settingsWriter view, naming the CONCRETE type it is expected to
// return.
//
// The type parameter is the point. Every NewWriter in the repo is declared as
// returning the narrow agent.SettingsWriter, so widening needs an assertion
// somewhere; making it `newWriter(o).(settingsWriter)` put that assertion at
// RUN time, inside a range expression, where a writer that stopped exposing
// SettingsPath produced a bare interface-conversion panic with no agent named
// — a compile-time contract spent as a runtime crash. Binding W instead makes
// the compiler check it at the call site: `concrete[*kiro.KiroWriter]` does
// not build, because KiroWriter's settings are genuinely multi-file and it has
// no SettingsPath at all. That is exactly the failure this table must produce
// for an agent it cannot yet cover.
func concrete[W settingsWriter](newWriter func(agent.SettingsOptions) agent.SettingsWriter) func(agent.SettingsOptions) settingsWriter {
	return func(o agent.SettingsOptions) settingsWriter { return newWriter(o).(W) }
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

// TestConformance_RefusesToOverwriteUnparseableSettings: a corrupt existing
// settings file must NEVER be overwritten. WriteSettings must refuse (return a
// non-nil error naming the offending file) and must leave the original bytes
// on disk exactly as they were. This is the opposite of what this test used to
// assert — it formerly required WriteSettings to succeed and apply hooks over
// the corrupt file, which is precisely the silent-data-loss shape production
// now refuses (see agent.RefuseCorrupt). claude-code and antigravity also back
// the corrupt bytes up to a sibling "<path>.corrupt-<unix-ts>" file before
// refusing; that backup is asserted per-engine below since codex's writer
// (internal/codex/settings.go loadSettings/load) returns a bare error with no
// backup file — a genuine behavioural divergence between engines, not
// something this test papers over.
func TestConformance_RefusesToOverwriteUnparseableSettings(t *testing.T) {
	const corrupt = "!!! not valid !!!"

	// engines whose writer backs the corrupt original up to a sibling
	// "<path>.corrupt-<ts>" file before refusing to write. codex is
	// deliberately absent: its loader (internal/codex/settings.go) returns a
	// bare "refusing to write over a config.toml ctxloom could not read"
	// error with no backup file at all.
	backsUpCorruptFile := map[string]bool{
		"claude-code": true,
		"antigravity": true,
		"codex":       false,
	}

	for _, a := range agentCases() {
		t.Run(a.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			w := a.newWriter(agent.SettingsOptions{FS: fs})
			path := w.SettingsPath(projectDir)
			require.NoError(t, afero.WriteFile(fs, path, []byte(corrupt), 0644))

			err := w.WriteSettings(standardHooks(), nil, nil, projectDir)
			require.Error(t, err, "WriteSettings must refuse rather than overwrite an unparseable prior file")
			assert.Contains(t, err.Error(), path, "the refusal error must name the offending file")

			data, readErr := afero.ReadFile(fs, path)
			require.NoError(t, readErr)
			assert.Equal(t, corrupt, string(data), "the original corrupt bytes must be left untouched, never overwritten")

			if backsUpCorruptFile[a.name] {
				entries, dirErr := afero.ReadDir(fs, filepath.Dir(path))
				require.NoError(t, dirErr)
				prefix := filepath.Base(path) + ".corrupt-"
				found := false
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), prefix) {
						found = true
						break
					}
				}
				assert.True(t, found, "%s must back the corrupt original up to a sibling %q file", a.name, prefix+"<unix-ts>")
			}
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
