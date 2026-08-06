package antigravity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

func TestAntigravityHookWriter_Paths(t *testing.T) {
	writer := &AntigravityHookWriter{}

	assert.Equal(t, "/project/.agents/hooks.json", writer.SettingsPath("/project"))
	assert.Equal(t, "/project/.agents/mcp_config.json", writer.MCPConfigPath("/project"))
}

func TestAntigravityHookWriter_WriteSettingsHooks(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool:  []wire.Hook{{Command: "./pre-tool.sh", Matcher: "run_command"}},
			PostTool: []wire.Hook{{Command: "./post-tool.sh", Matcher: "write_to_file"}},
		},
	}

	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)

	var settings map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &settings))
	require.Contains(t, settings, "hooks")

	var hooks map[string][]antigravityHookGroup
	require.NoError(t, json.Unmarshal(settings["hooks"], &hooks))
	require.Contains(t, hooks, "PreToolUse")
	require.Contains(t, hooks, "PostToolUse")
	require.Len(t, hooks["PreToolUse"], 1)
	assert.Equal(t, "run_command", hooks["PreToolUse"][0].Matcher)
	require.Len(t, hooks["PreToolUse"][0].Hooks, 1)
	assert.Equal(t, "command", hooks["PreToolUse"][0].Hooks[0].Type)
	assert.Equal(t, "./pre-tool.sh", hooks["PreToolUse"][0].Hooks[0].Command)
	assert.Equal(t, antigravityCtxloomHookName, hooks["PreToolUse"][0].Hooks[0].Name)
	assert.Zero(t, hooks["PreToolUse"][0].Hooks[0].Timeout, "timeout is never written — agy's own default applies")
}

// TestAntigravityHookWriter_PreShellPostFileEdit verifies the unified
// PreShell / PostFileEdit hooks map onto PreToolUse/PostToolUse with the
// default agy tool-name matchers.
func TestAntigravityHookWriter_PreShellPostFileEdit(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreShell:     []wire.Hook{{Command: "ctxloom hook pre-shell"}},
		PostFileEdit: []wire.Hook{{Command: "ctxloom hook post-edit"}},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	hooks := readHooks(t, fs)
	require.Contains(t, hooks, "PreToolUse")
	require.Contains(t, hooks, "PostToolUse")
	assert.Equal(t, antigravityShellMatcher, hooks["PreToolUse"][0].Matcher)
	assert.Equal(t, antigravityFileEditMatcher, hooks["PostToolUse"][0].Matcher)
}

// TestAntigravityHookWriter_MatchersPinAgyToolVocabulary pins the default
// matchers to the verified agy tool names; the constants are built from the
// hooks_wire.go vocabulary, so a wire-constant edit shows up here.
func TestAntigravityHookWriter_MatchersPinAgyToolVocabulary(t *testing.T) {
	assert.Equal(t, "run_command|execute_command", antigravityShellMatcher)
	assert.Equal(t, "write_to_file|replace_file_content", antigravityFileEditMatcher)
}

// TestAntigravityHookWriter_SkipsNonCommandHooks verifies prompt/agent hooks
// are skipped rather than mangled into dead {"type":"command","command":""}
// entries — agy only executes command hooks. Empty Type is the wire default
// and means command.
func TestAntigravityHookWriter_SkipsNonCommandHooks(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreTool: []wire.Hook{
			{Type: "prompt", Prompt: "be careful"},
			{Type: "agent", Prompt: "review this tool call"},
			{Type: "command", Command: "ctxloom hook pre-tool"},
			{Command: "ctxloom hook pre-tool-default"}, // empty Type = command
		},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	hooks := readHooks(t, fs)
	require.Contains(t, hooks, "PreToolUse")
	var commands []string
	for _, g := range hooks["PreToolUse"] {
		for _, e := range g.Hooks {
			commands = append(commands, e.Command)
		}
	}
	assert.ElementsMatch(t, []string{"ctxloom hook pre-tool", "ctxloom hook pre-tool-default"}, commands)

	data, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"command": ""`, "no dead empty-command entries")
}

// TestAntigravityHookWriter_SkipsEmptyCommandHook pins the PAYLOAD: no entry
// ctxloom writes may carry an empty command. agy's hook contract makes
// `command` REQUIRED (verified against agy's own bundled documentation,
// ~/.gemini/antigravity-cli/builtin/skills/agy-customizations/docs/hooks.md on
// agy 1.1.5: "command (string, required)"), so an entry without one is a
// live-looking handler that executes nothing — a real silent no-op.
//
// The reachable path is a hook authored with a prompt and NO explicit type:
// the wire default for Type is "" which MEANS command, so the non-command
// guard above lets it through with nothing to run. Asserting on the decoded
// entries (not on the raw bytes) is deliberate — `command` is `omitempty`, so
// the sibling test's `NotContains("\"command\": \"\"")` byte check can never
// fail no matter how many dead entries are written.
func TestAntigravityHookWriter_SkipsEmptyCommandHook(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{PreTool: []wire.Hook{
			{Prompt: "be careful"}, // untyped prompt hook: Type "" reads as command
			{Command: "ctxloom hook pre-tool"},
		}},
		Plugins: map[string]wire.BackendHooks{
			"antigravity": {"PostToolUse": []wire.Hook{{Matcher: "write_to_file"}}},
		},
	}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	hooks := readHooks(t, fs)
	var written []string
	for event, groups := range hooks {
		for _, g := range groups {
			for _, e := range g.Hooks {
				assert.NotEmpty(t, e.Command, "%s carries an entry with no command to run", event)
				written = append(written, e.Command)
			}
		}
	}
	assert.Equal(t, []string{"ctxloom hook pre-tool"}, written)
}

// TestAntigravityHookWriter_CompanionHookIdempotent verifies exact-duplicate
// suppression for companion-binary hooks: an identical command already
// installed under the same event by a non-ctxloom entry (e.g. ltk registered
// `ltk evaluate` itself, no marker) must not duplicate when ctxloom adds the
// same hook — same semantics as the claude writer's removeExactCommand. A
// user variant of the same binary with different args survives.
func TestAntigravityHookWriter_CompanionHookIdempotent(t *testing.T) {
	fs := afero.NewMemMapFs()
	existing := `{"hooks":{"PreToolUse":[{"matcher":"run_command|execute_command","hooks":[
		{"type":"command","command":"ltk evaluate"},
		{"type":"command","command":"ltk evaluate --config .ltk/config.yaml"}]}]}}`
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/hooks.json", []byte(existing), 0644))

	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreShell: []wire.Hook{{Command: "ltk evaluate"}},
	}}
	for range 3 {
		require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))
	}

	hooks := readHooks(t, fs)
	exact, variant := 0, 0
	for _, g := range hooks["PreToolUse"] {
		for _, e := range g.Hooks {
			switch e.Command {
			case "ltk evaluate":
				exact++
			case "ltk evaluate --config .ltk/config.yaml":
				variant++
			}
		}
	}
	assert.Equal(t, 1, exact, "companion hook must not duplicate across re-applies")
	assert.Equal(t, 1, variant, "user's own variant of the same binary must survive")
}

// jsonProbeObject builds a JSON object carrying one entry for every
// json-encodable field of shape (a type-plausible value per kind), plus a key no
// struct declares. Derived from the struct by reflection so it stays honest as
// the struct changes.
func jsonProbeObject(t *testing.T, shape any) []byte {
	t.Helper()
	typ := reflect.TypeOf(shape)
	fields := map[string]json.RawMessage{"fieldAgyAddedLater": json.RawMessage(`"keep me"`)}
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		switch f.Type.Kind() {
		case reflect.String:
			fields[name] = json.RawMessage(`"probe"`)
		case reflect.Int:
			fields[name] = json.RawMessage(`7`)
		case reflect.Bool:
			fields[name] = json.RawMessage(`true`)
		default:
			fields[name] = json.RawMessage(`null`)
		}
	}
	data, err := json.Marshal(fields)
	require.NoError(t, err)
	return data
}

// TestHookShapes_UnknownCaptureExcludesEveryDeclaredField pins a real bug: the
// round-trip preservation in UnmarshalJSON captures unknown keys by REMOVING the
// known ones from the raw object, so the set it removes has to be exactly the
// set of fields the struct declares. Enumerating that set by hand — as two
// literal string lists did — has no compiler link to the structs, so a field
// added later silently lands in both the typed shape and the "unknown" carry-over
// map. This test derives the probe object from the structs themselves, so it
// fails the moment the two sets diverge.
func TestHookShapes_UnknownCaptureExcludesEveryDeclaredField(t *testing.T) {
	onlyUnknown := map[string]json.RawMessage{"fieldAgyAddedLater": json.RawMessage(`"keep me"`)}

	t.Run("group", func(t *testing.T) {
		var g antigravityHookGroup
		require.NoError(t, json.Unmarshal(jsonProbeObject(t, antigravityHookGroupShape{}), &g))
		assert.Equal(t, onlyUnknown, g.extra)
		assert.Equal(t, "probe", g.Matcher, "the declared fields still decode into the shape")
	})

	t.Run("entry", func(t *testing.T) {
		var e antigravityHookEntry
		require.NoError(t, json.Unmarshal(jsonProbeObject(t, antigravityHookEntryShape{}), &e))
		assert.Equal(t, onlyUnknown, e.extra)
		assert.Equal(t, "probe", e.Type)
		assert.Equal(t, 7, e.Timeout)
	})
}

// TestAntigravityHookWriter_PreservesUnknownGroupAndEntryFields verifies
// fields agy adds later at the group or entry level round-trip a rewrite
// instead of being silently dropped, and that preservation stays
// byte-idempotent across re-applies.
func TestAntigravityHookWriter_PreservesUnknownGroupAndEntryFields(t *testing.T) {
	fs := afero.NewMemMapFs()
	userHooks := `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "run_command", "futureGroupField": {"nested": true}, "hooks": [
					{"type": "command", "command": "/usr/local/bin/my-guard", "timeout": 30, "futureEntryField": [1, 2]}
				]}
			]
		}
	}`
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/hooks.json", []byte(userHooks), 0644))

	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreShell: []wire.Hook{{Command: "ctxloom hook pre-shell"}},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	first, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.Contains(t, string(first), `"futureGroupField"`)
	assert.Contains(t, string(first), `"futureEntryField"`)
	// Known fields keep their shape next to the preserved unknowns.
	assert.Contains(t, string(first), `"command": "/usr/local/bin/my-guard"`)
	assert.Contains(t, string(first), `"timeout": 30`)

	// Re-apply: byte-identical (reconcile, not append; extras merge stably).
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))
	second, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))

	// Remove: ctxloom entry gone, user entry with unknown fields intact.
	require.NoError(t, writer.RemoveSettings("/project"))
	final, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.Contains(t, string(final), `"futureGroupField"`)
	assert.Contains(t, string(final), `"futureEntryField"`)
	assert.NotContains(t, string(final), "ctxloom hook pre-shell")
}

// TestAntigravityHookWriter_SessionEventsPassThrough verifies SessionStart /
// SessionEnd entries are written verbatim (agy v1.0.7 silently skips them; a
// future agy may load them).
func TestAntigravityHookWriter_SessionEventsPassThrough(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom session bind"}},
		SessionEnd:   []wire.Hook{{Command: "ctxloom session end"}},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	hooks := readHooks(t, fs)
	assert.Contains(t, hooks, "SessionStart")
	assert.Contains(t, hooks, "SessionEnd")
}

// TestAntigravityHookWriter_PreToolFallbackDiverts verifies a session_start
// hook declared pre_tool_fallback registers under PreToolUse (the only event
// where it can fire on agy) and NOT under SessionStart — diverted, not
// duplicated, so a future agy adding SessionStart can't double-fire it.
func TestAntigravityHookWriter_PreToolFallbackDiverts(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", PreToolFallback: true}},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	hooks := readHooks(t, fs)
	assert.NotContains(t, hooks, "SessionStart")
	require.Contains(t, hooks, "PreToolUse")
	require.Len(t, hooks["PreToolUse"], 1)
	assert.Equal(t, ".*", hooks["PreToolUse"][0].Matcher)
	assert.Equal(t, "ctxloom hook session-bind", hooks["PreToolUse"][0].Hooks[0].Command)
}

// TestAntigravityHookWriter_PreservesUserEntries verifies user hooks, unknown
// top-level fields, and user MCP servers (including remote serverUrl entries
// with fields ctxloom does not model) survive a write/remove cycle.
func TestAntigravityHookWriter_PreservesUserEntries(t *testing.T) {
	fs := afero.NewMemMapFs()
	userHooks := `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "run_command", "hooks": [{"type": "command", "command": "/usr/local/bin/my-guard", "timeout": 30}]}
			]
		},
		"futureSetting": {"nested": true}
	}`
	userMCP := `{
		"mcpServers": {
			"remote-thing": {"serverUrl": "https://example.com/mcp", "headers": {"X-Auth": "secret"}}
		}
	}`
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/hooks.json", []byte(userHooks), 0644))
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/mcp_config.json", []byte(userMCP), 0644))

	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreShell: []wire.Hook{{Command: "ctxloom hook pre-shell"}},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	// User hook entry survives, with its timeout intact, alongside ctxloom's.
	hooks := readHooks(t, fs)
	require.Len(t, hooks["PreToolUse"], 2)
	var userEntry *antigravityHookEntry
	for _, g := range hooks["PreToolUse"] {
		for i := range g.Hooks {
			if g.Hooks[i].Command == "/usr/local/bin/my-guard" {
				userEntry = &g.Hooks[i]
			}
		}
	}
	require.NotNil(t, userEntry, "user hook entry preserved")
	assert.Equal(t, 30, userEntry.Timeout)

	// Unknown top-level field survives.
	data, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	assert.Contains(t, top, "futureSetting")

	// Remote MCP server survives raw, headers and all, next to ctxloom's.
	mcpData, err := afero.ReadFile(fs, "/project/.agents/mcp_config.json")
	require.NoError(t, err)
	var mcpTop map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(mcpData, &mcpTop))
	require.Contains(t, mcpTop["mcpServers"], "remote-thing")
	assert.Contains(t, string(mcpTop["mcpServers"]["remote-thing"]), "serverUrl")
	assert.Contains(t, string(mcpTop["mcpServers"]["remote-thing"]), "X-Auth")
	require.Contains(t, mcpTop["mcpServers"], AppMCPServerName)

	// Remove: ctxloom entries gone, user entries intact.
	require.NoError(t, writer.RemoveSettings("/project"))
	hooks = readHooks(t, fs)
	require.Len(t, hooks["PreToolUse"], 1)
	assert.Equal(t, "/usr/local/bin/my-guard", hooks["PreToolUse"][0].Hooks[0].Command)

	mcpData, err = afero.ReadFile(fs, "/project/.agents/mcp_config.json")
	require.NoError(t, err)
	mcpTop = nil
	require.NoError(t, json.Unmarshal(mcpData, &mcpTop))
	assert.Contains(t, mcpTop["mcpServers"], "remote-thing")
	assert.NotContains(t, mcpTop["mcpServers"], AppMCPServerName)
}

// TestAntigravityHookWriter_MCPLedgerReconcilesRenamesAndRemovals pins the
// managed-MCP ownership ledger: agy rejects in-file marker fields (verified —
// they hang headless agy), so managed names live in the .ctxloom-mcp-managed
// sidecar. A server renamed or removed from config between applies must not
// linger in mcp_config.json — a stale stdio entry permanently hangs agy.
func TestAntigravityHookWriter_MCPLedgerReconcilesRenamesAndRemovals(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	userServer := `{"mcpServers": {"user-thing": {"command": "user-bin"}}}`
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/mcp_config.json", []byte(userServer), 0644))

	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"old-name": {Command: "tool-v1"},
	}}
	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, mcp, nil, "/project"))

	servers := readMCPServers(t, fs)
	assert.Contains(t, servers, "old-name")
	assert.Contains(t, servers, "user-thing")

	// Rename the managed server in config: the old entry must disappear.
	mcp = &wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"new-name": {Command: "tool-v2"},
	}}
	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, mcp, nil, "/project"))

	servers = readMCPServers(t, fs)
	assert.NotContains(t, servers, "old-name", "renamed managed server must not linger")
	assert.Contains(t, servers, "new-name")
	assert.Contains(t, servers, "user-thing", "user server never touched")

	// Uninstall: every managed server (and the ledger) goes; user entry stays.
	require.NoError(t, writer.RemoveSettings("/project"))
	servers = readMCPServers(t, fs)
	assert.NotContains(t, servers, "new-name")
	assert.NotContains(t, servers, AppMCPServerName)
	assert.Contains(t, servers, "user-thing")
	ledgerExists, _ := afero.Exists(fs, filepath.Join("/project/.agents", ledger.Name))
	assert.False(t, ledgerExists, "ledger removed with the last managed server")
}

// readMCPServers unmarshals the server map from the written mcp_config.json.
func readMCPServers(t *testing.T, fs afero.Fs) map[string]json.RawMessage {
	t.Helper()
	data, err := afero.ReadFile(fs, "/project/.agents/mcp_config.json")
	require.NoError(t, err)
	var top map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	return top["mcpServers"]
}

// TestAntigravityHookWriter_Idempotent verifies double-apply produces
// identical files (reconcile, not append).
func TestAntigravityHookWriter_Idempotent(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreShell: []wire.Hook{{Command: "ctxloom hook pre-shell"}},
	}}

	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))
	first, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	firstMCP, err := afero.ReadFile(fs, "/project/.agents/mcp_config.json")
	require.NoError(t, err)

	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))
	second, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	secondMCP, err := afero.ReadFile(fs, "/project/.agents/mcp_config.json")
	require.NoError(t, err)

	assert.Equal(t, string(first), string(second))
	assert.Equal(t, string(firstMCP), string(secondMCP))
}

// This test used to assert the OPPOSITE — that a corrupt hooks.json did not
// block hook application, under a "fault-tolerance contract". What
// it actually pinned was the destruction of the user's hook configuration:
// loadHooksFile returned an empty structure and saveHooksFile wrote it back
// as a ctxloom-only file. Not blocking the launch is worth having; achieving
// it by deleting what you could not read is not.
//
// The corrected contract: an unreadable hooks.json stops the write and leaves
// the file (and a .corrupt backup) for the user to fix.
func TestAntigravityHookWriter_CorruptHooksFile_DoesNotApplyOverTheTopOfIt(t *testing.T) {
	fs := afero.NewMemMapFs()
	corrupt := "{not valid json"
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/hooks.json", []byte(corrupt), 0644))

	writer := &AntigravityHookWriter{FS: fs}
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreTool: []wire.Hook{{Command: "ctxloom hook pre-tool"}},
	}}
	require.Error(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.Equal(t, corrupt, string(data), "the user's hooks.json must survive verbatim")
}

// TestAntigravityHookWriter_EmptyMCPFileTolerated verifies the zero-byte
// mcp_config.json files agy itself creates load as empty.
func TestAntigravityHookWriter_EmptyMCPFileTolerated(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/mcp_config.json", []byte(""), 0644))

	writer := &AntigravityHookWriter{FS: fs}
	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, nil, nil, "/project"))

	mcpData, err := afero.ReadFile(fs, "/project/.agents/mcp_config.json")
	require.NoError(t, err)
	var mcpTop map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(mcpData, &mcpTop))
	assert.Contains(t, mcpTop["mcpServers"], AppMCPServerName)
}

// TestAntigravityHookWriter_NoStrayEmptyHooksFile verifies that applying a
// profile with no managed hooks (the context-injection hook is diverted to
// AGENTS.md, so hooks stay empty) does not create a stray empty `{}`
// hooks.json — matching saveMCPFile / reconcileManagedContext, which never
// create empty files (antigravity-code-01-002).
func TestAntigravityHookWriter_NoStrayEmptyHooksFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, nil, nil, "/project"))

	hooksExists, err := afero.Exists(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.False(t, hooksExists, "empty hooks must not create a stray hooks.json")

	// The MCP server is still auto-registered, so its file is created as usual.
	mcpExists, err := afero.Exists(fs, "/project/.agents/mcp_config.json")
	require.NoError(t, err)
	assert.True(t, mcpExists, "ctxloom MCP server should still be registered")
}

// TestAntigravityHookWriter_RemoveLeavesAbsentFilesAbsent verifies uninstall
// never creates files.
func TestAntigravityHookWriter_RemoveLeavesAbsentFilesAbsent(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	require.NoError(t, writer.RemoveSettings("/project"))

	for _, p := range []string{"/project/.agents/hooks.json", "/project/.agents/mcp_config.json"} {
		exists, err := afero.Exists(fs, p)
		require.NoError(t, err)
		assert.False(t, exists, p)
	}
}

// errUnreadable stands in for the class of filesystem failure that is NOT
// "absent": a permission denial, an I/O error, a path that stopped being
// traversable. afero.Exists reports exactly this class as an error and reports a
// genuinely missing file as (false, nil), so discarding its error is what turns
// "I could not tell" into "it is not there".
var errUnreadable = errors.New("permission denied by the test filesystem")

// failFS fails Stat (and/or Open) for paths ending in the configured suffix,
// delegating everything else. Injecting at Stat is what reaches the swallowed
// afero.Exists errors; injecting at Open reaches the swallowed afero.ReadFile
// error in Status.
type failFS struct {
	afero.Fs
	statFail string
	openFail string
}

func (f failFS) Stat(name string) (os.FileInfo, error) {
	if f.statFail != "" && strings.HasSuffix(filepath.ToSlash(name), f.statFail) {
		return nil, errUnreadable
	}
	return f.Fs.Stat(name)
}

func (f failFS) Open(name string) (afero.File, error) {
	if f.openFail != "" && strings.HasSuffix(filepath.ToSlash(name), f.openFail) {
		return nil, errUnreadable
	}
	return f.Fs.Open(name)
}

// TestAntigravityHookWriter_UnreadableFilesAreReportedNotAssumedAbsent pins a
// real bug: four filesystem errors were discarded with `_`, each collapsing "I
// could not tell whether this file is there" into "it is not there". The
// consequences are all silent: RemoveSettings reports success having cleared
// nothing, Status reports an unwired project, and WriteSettings decides not to
// write on the strength of a check that failed.
func TestAntigravityHookWriter_UnreadableFilesAreReportedNotAssumedAbsent(t *testing.T) {
	const managed = `{"hooks":{"PreToolUse":[{"matcher":".*","hooks":[{"type":"command","command":"ctxloom hook pre-tool","name":"ctxloom-managed"}]}]}}`

	t.Run("RemoveSettings", func(t *testing.T) {
		base := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(base, "/project/.agents/hooks.json", []byte(managed), 0644))
		writer := &AntigravityHookWriter{FS: failFS{Fs: base, statFail: "hooks.json"}}

		err := writer.RemoveSettings("/project")
		require.Error(t, err, "uninstall must not report success when it could not read hooks.json")
		assert.ErrorIs(t, err, errUnreadable)

		// The managed hook is still there — proving the reported failure is real
		// and not a cosmetic error on an otherwise-completed removal.
		data, readErr := afero.ReadFile(base, "/project/.agents/hooks.json")
		require.NoError(t, readErr)
		assert.Contains(t, string(data), "ctxloom hook pre-tool")
	})

	t.Run("Status/hooks", func(t *testing.T) {
		base := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(base, "/project/.agents/hooks.json", []byte(managed), 0644))
		writer := &AntigravityHookWriter{FS: failFS{Fs: base, statFail: "hooks.json"}}

		_, err := writer.Status("/project")
		require.Error(t, err, "status must not report an unwired project on an unreadable hooks.json")
		assert.ErrorIs(t, err, errUnreadable)
	})

	t.Run("Status/AGENTS.md", func(t *testing.T) {
		base := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(base, "/project/.agents/AGENTS.md",
			[]byte(managedContextBegin+"\ncontext\n"+managedContextEnd+"\n"), 0644))
		writer := &AntigravityHookWriter{FS: failFS{Fs: base, openFail: "AGENTS.md"}}

		_, err := writer.Status("/project")
		require.Error(t, err, "status must not report 'no managed context' on an unreadable AGENTS.md")
		assert.ErrorIs(t, err, errUnreadable)
	})

	t.Run("WriteSettings/emptyHooks", func(t *testing.T) {
		base := afero.NewMemMapFs()
		writer := &AntigravityHookWriter{FS: failFS{Fs: base, statFail: "hooks.json"}}

		// Nothing to persist, so the writer consults "does hooks.json already
		// exist?" to decide between rewriting it and leaving no stray file. That
		// check failing is not an answer.
		err := writer.WriteSettings(&wire.HooksConfig{}, nil, nil, "/project")
		require.Error(t, err)
		assert.ErrorIs(t, err, errUnreadable)
	})
}

func TestAntigravityHookWriter_Status(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	status, err := writer.Status("/project")
	require.NoError(t, err)
	assert.False(t, status.Wired())

	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreTool: []wire.Hook{{Command: "ctxloom hook pre-tool"}},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	status, err = writer.Status("/project")
	require.NoError(t, err)
	assert.True(t, status.SettingsExists)
	assert.True(t, status.HooksPresent)
	assert.True(t, status.MCPPresent)
	assert.True(t, status.Wired())

	require.NoError(t, writer.RemoveSettings("/project"))
	status, err = writer.Status("/project")
	require.NoError(t, err)
	assert.False(t, status.HooksPresent)
	assert.False(t, status.MCPPresent)
}

// writeContextFixture writes a content-addressed context file the way the
// context provider does, so the writer's AGENTS.md materialization can read it.
func writeContextFixture(t *testing.T, fs afero.Fs, hash, content string) {
	t.Helper()
	path := "/project/.ctxloom/cache/context/" + hash + ".md"
	require.NoError(t, afero.WriteFile(fs, path, []byte(content), 0644))
}

// contextHooksCfg builds a hooks config carrying the context-injection hook
// the way agent.NewContextInjectionHooks marks it.
func contextHooksCfg(hash string) *wire.HooksConfig {
	return &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context " + hash, ContextHash: hash}},
	}}
}

// TestAntigravityHookWriter_MaterializesContextIntoAgentsMD pins the agy
// context-delivery channel: agy fires no SessionStart hooks, so the
// context-injection hook must NOT land in hooks.json — the assembled context
// is written into .agents/AGENTS.md (which agy reads, verified) instead.
func TestAntigravityHookWriter_MaterializesContextIntoAgentsMD(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	writeContextFixture(t, fs, "abc123", "# Project Context\nthe secret color is vermilion")

	require.NoError(t, writer.WriteSettings(contextHooksCfg("abc123"), nil, nil, "/project"))

	data, err := afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), managedContextBegin)
	assert.Contains(t, string(data), "the secret color is vermilion")
	assert.Contains(t, string(data), managedContextEnd)

	// The injection hook must not appear as a dead hooks.json entry. With no
	// other managed hooks, hooks.json must not be created at all: the writer
	// leaves no stray empty `{}` file for a context-only profile
	// (antigravity-code-01-002).
	hooksExists, err := afero.Exists(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.False(t, hooksExists, "no stray hooks.json for a context-only profile")
}

// TestAntigravityHookWriter_ContextReconcileAndUserContent verifies the
// managed section is replaced on re-apply, removed when no context hook is
// present, and that user-authored AGENTS.md content outside the markers
// survives every step.
func TestAntigravityHookWriter_ContextReconcileAndUserContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	require.NoError(t, afero.WriteFile(fs, "/project/.agents/AGENTS.md", []byte("# My rules\nalways frobnicate\n"), 0644))
	writeContextFixture(t, fs, "hash1", "context one")
	writeContextFixture(t, fs, "hash2", "context two")

	require.NoError(t, writer.WriteSettings(contextHooksCfg("hash1"), nil, nil, "/project"))
	data, _ := afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	assert.Contains(t, string(data), "always frobnicate")
	assert.Contains(t, string(data), "context one")

	// Re-apply with a new hash: section replaced, not appended.
	require.NoError(t, writer.WriteSettings(contextHooksCfg("hash2"), nil, nil, "/project"))
	data, _ = afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	assert.Contains(t, string(data), "context two")
	assert.NotContains(t, string(data), "context one")
	assert.Contains(t, string(data), "always frobnicate")

	// Apply without a context hook: section removed, user content intact.
	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, nil, nil, "/project"))
	data, _ = afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	assert.NotContains(t, string(data), managedContextBegin)
	assert.Contains(t, string(data), "always frobnicate")
}

// TestAntigravityHookWriter_ContextRemovedWithSettings verifies RemoveSettings
// strips the managed section and deletes a file that was wholly ctxloom's.
func TestAntigravityHookWriter_ContextRemovedWithSettings(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	writeContextFixture(t, fs, "abc123", "managed context only")

	require.NoError(t, writer.WriteSettings(contextHooksCfg("abc123"), nil, nil, "/project"))
	status, err := writer.Status("/project")
	require.NoError(t, err)
	assert.True(t, status.HooksPresent, "managed context section counts as wired")

	require.NoError(t, writer.RemoveSettings("/project"))
	exists, err := afero.Exists(fs, "/project/.agents/AGENTS.md")
	require.NoError(t, err)
	assert.False(t, exists, "a wholly managed AGENTS.md is removed, not left empty")
}

// TestAntigravityHookWriter_ChunkedContextMaterializesOnce verifies N chunked
// injection hooks (one hash) yield a single managed section — chunking is a
// hook-harness workaround that does not apply to file delivery.
func TestAntigravityHookWriter_ChunkedContextMaterializesOnce(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}
	writeContextFixture(t, fs, "bighash", "whole content")

	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{
			{Command: "ctxloom hook inject-context --part 1 --of 2 bighash", ContextHash: "bighash"},
			{Command: "ctxloom hook inject-context --part 2 --of 2 bighash", ContextHash: "bighash"},
		},
	}}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, "/project/.agents/AGENTS.md")
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), managedContextBegin))
	assert.Equal(t, 1, strings.Count(string(data), "whole content"))
}

// readHooks unmarshals the hooks map from the written hooks.json.
func readHooks(t *testing.T, fs afero.Fs) map[string][]antigravityHookGroup {
	t.Helper()
	data, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	require.Contains(t, top, "hooks")
	var hooks map[string][]antigravityHookGroup
	require.NoError(t, json.Unmarshal(top["hooks"], &hooks))
	return hooks
}

// The antigravity twin of the same bug class in the claude and codex writers:
// loadHooksFile returned an empty-but-valid structure on a parse failure and
// saveHooksFile then persisted it, so an unparseable hooks.json was replaced
// with a ctxloom-only file — the user's entire hook configuration destroyed,
// behind a warning that called itself a "fault-tolerance contract".
func TestAntigravityHookWriter_MalformedHooksFile_FailsLoudAndBacksUp(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	path := "/project/.agents/hooks.json"
	require.NoError(t, fs.MkdirAll("/project/.agents", 0755))
	corrupt := `{ "hooks": { "preToolUse": [ }`
	require.NoError(t, afero.WriteFile(fs, path, []byte(corrupt), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "./pre.sh"}}},
	}
	require.Error(t, writer.WriteSettings(cfg, nil, nil, "/project"),
		"an unreadable hooks.json must stop the write, not be replaced by a ctxloom-only one")

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, corrupt, string(data), "the user's hooks.json must be left exactly as it was")

	entries, err := afero.ReadDir(fs, "/project/.agents")
	require.NoError(t, err)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "hooks.json.corrupt-") {
			found = true
		}
	}
	assert.True(t, found, "expected a hooks.json.corrupt-<timestamp> backup of the original bytes")
}

// The nested case: the file parses but "hooks" is the wrong shape. Warning
// and continuing dropped every hook the user had.
func TestAntigravityHookWriter_MalformedHooksField_FailsLoudRatherThanDropping(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	path := "/project/.agents/hooks.json"
	require.NoError(t, fs.MkdirAll("/project/.agents", 0755))
	corrupt := `{"hooks": "not-an-object", "somethingElse": true}`
	require.NoError(t, afero.WriteFile(fs, path, []byte(corrupt), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "./pre.sh"}}},
	}
	require.Error(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, corrupt, string(data))
}

// An ABSENT hooks.json is legitimately nothing to preserve and must still
// write cleanly — the guard must not block a first run.
func TestAntigravityHookWriter_AbsentHooksFile_StillWrites(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &AntigravityHookWriter{FS: fs}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "./pre.sh"}}},
	}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.Contains(t, string(data), "pre.sh")
}

// TestAntigravityHookWriter_PreservesLargeNumbersInHooksFile pins that the
// hooks.json rewrite is value-preserving: a number the user put beside the
// hooks block must come back out as the same literal. A generic decode on the
// way to the canonicaliser rounds anything past float64's exact range, which
// silently rewrites the user's own file.
func TestAntigravityHookWriter_PreservesLargeNumbersInHooksFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := filepath.Join("/project", AgentsDir, "hooks.json")
	original := `{"timeoutMs": 1234567890123456789, "nested": {"id": 9223372036854775807}}`
	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0644))

	w := &AntigravityHookWriter{FS: fs}
	require.NoError(t, w.WriteSettings(&wire.HooksConfig{
		Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "./pre.sh", Matcher: "Bash"}}},
	}, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "1234567890123456789", "a preserved top-level number must survive the rewrite exactly")
	assert.Contains(t, got, "9223372036854775807", "a nested preserved number must survive too")
}
