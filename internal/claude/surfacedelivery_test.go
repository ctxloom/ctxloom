package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// fakePlacement is defined in contextdelivery_test.go (same package): a local
// agent.Placement double whose Dir() returns a fixed temp dir.

// mcpServersOf reads .mcp.json under dir and returns its mcpServers map.
func mcpServersOf(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	servers, _ := cfg["mcpServers"].(map[string]any)
	return servers
}

// TestFileTemplateDelivery_DeliverMCP verifies the file-template strategy writes
// the MCP surface into the injected Placement identically to writeMCPConfig, and
// that Cleanup reverts ctxloom-owned servers while preserving user servers.
func TestFileTemplateDelivery_DeliverMCP(t *testing.T) {
	mcp := &wire.MCPConfig{
		Servers: map[string]wire.MCPServer{
			"config-server": {Command: "config-cmd", Args: []string{"--flag"}},
		},
	}
	bundle := map[string]wire.MCPServer{
		"bundle-server": {Command: "bundle-cmd", SCM: "ctxloom-bundle:test"},
	}

	// Delivery target.
	deliverDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: deliverDir}, nil)
	handle, err := d.DeliverMCP(mcp, bundle)
	require.NoError(t, err)

	// Control: the existing writer targeted at a separate dir must produce the
	// same on-disk bytes.
	controlDir := t.TempDir()
	require.NoError(t, (&ClaudeCodeHookWriter{}).writeMCPConfig(controlDir, mcp, bundle))

	got, err := os.ReadFile(filepath.Join(deliverDir, ".mcp.json"))
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join(controlDir, ".mcp.json"))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "DeliverMCP must write the same .mcp.json as writeMCPConfig")

	// The ctxloom-owned servers are present after delivery.
	servers := mcpServersOf(t, deliverDir)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")
	assert.Contains(t, servers, AppMCPServerName)

	// Cleanup removes exactly the ctxloom-marked servers.
	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, deliverDir)
	assert.NotContains(t, servers, "config-server")
	assert.NotContains(t, servers, "bundle-server")
	assert.NotContains(t, servers, AppMCPServerName)
}

// TestFileTemplateDelivery_DeliverMCP_PreservesUserServers verifies a
// pre-existing user server (no _ctxloom marker) survives both delivery and
// cleanup while ctxloom servers come and go around it.
func TestFileTemplateDelivery_DeliverMCP_PreservesUserServers(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]any{
		"mcpServers": map[string]any{
			"my-server": map[string]any{"command": "/usr/local/bin/my-mcp"},
		},
	}
	data, err := json.Marshal(existing)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".mcp.json"), data, 0o644))

	d := newFileTemplateDelivery(fakePlacement{dir: dir}, nil)
	handle, err := d.DeliverMCP(nil, nil) // nil mcp still auto-registers ctxloom
	require.NoError(t, err)

	servers := mcpServersOf(t, dir)
	assert.Contains(t, servers, "my-server")
	assert.Contains(t, servers, AppMCPServerName)

	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, dir)
	assert.Contains(t, servers, "my-server", "user server must survive cleanup")
	assert.NotContains(t, servers, AppMCPServerName, "ctxloom server must be reverted")
}

// TestFileTemplateDelivery_DeliverCommands verifies the commands surface is written
// into the injected Placement identically to WriteCommandFiles, and that Cleanup
// reverts the manifest-tracked set while preserving user-authored commands.
func TestFileTemplateDelivery_DeliverCommands(t *testing.T) {
	commands := []agent.CommandExport{
		{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		{Name: "simple", Content: "Simple command", Enabled: true},
	}

	deliverDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: deliverDir}, nil)
	handle, err := d.DeliverCommands(commands)
	require.NoError(t, err)

	// Control: the existing writer targeted at a separate dir.
	controlDir := t.TempDir()
	require.NoError(t, WriteCommandFiles(controlDir, commands))

	for _, rel := range []string{"review.md", "simple.md", ledger.Name} {
		got, err := os.ReadFile(filepath.Join(deliverDir, ".claude", "commands", rel))
		require.NoError(t, err, "DeliverCommands must write %s", rel)
		want, err := os.ReadFile(filepath.Join(controlDir, ".claude", "commands", rel))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), "DeliverCommands must match WriteCommandFiles for %s", rel)
	}

	// Seed a user-authored command that ctxloom does not track.
	userCmd := filepath.Join(deliverDir, ".claude", "commands", "user.md")
	require.NoError(t, os.WriteFile(userCmd, []byte("mine"), 0o644))

	// Cleanup removes the manifest-tracked set (and manifest), not the user file.
	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", "review.md"))
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", "simple.md"))
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", ledger.Name))
	assert.FileExists(t, userCmd, "user-authored command must survive cleanup")
}

// writeRenderedHomeCommand pre-seeds homeDir/.claude/commands/<name>.md with
// exactly the bytes WriteCommandFiles would render for cmd, so a dedup check
// against it is a true byte-identical comparison rather than a coincidence.
func writeRenderedHomeCommand(t *testing.T, homeDir string, cmd agent.CommandExport) {
	t.Helper()
	homeCommandsDir := filepath.Join(homeDir, ".claude", "commands")
	require.NoError(t, os.MkdirAll(homeCommandsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(homeCommandsDir, cmd.Name+".md"),
		[]byte(TransformToClaudeCommand(cmd)), 0o644))
}

// TestFileTemplateDelivery_DeliverCommands_DedupsIdenticalHomeCopy verifies the
// wiring added to close the WithDedupHomeDir gap (never called in production
// before this fix, which left the option unwired): a project delivery skips a
// command whose rendered bytes are byte-identical to the same-named file already
// in the user-global ~/.claude/commands (here faked via $HOME), and the skip
// is not manifest-tracked, while a project-unique command still lands normally.
func TestFileTemplateDelivery_DeliverCommands_DedupsIdenticalHomeCopy(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	dup := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, dup)

	projectDir := t.TempDir()
	keep := agent.CommandExport{Name: "keep", Content: "Project-only command", Enabled: true}

	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, nil)
	_, err := d.DeliverCommands([]agent.CommandExport{dup, keep})
	require.NoError(t, err)

	commandsDir := filepath.Join(projectDir, ".claude", "commands")
	assert.NoFileExists(t, filepath.Join(commandsDir, "recover.md"), "identical home copy must not be duplicated into the project scope")
	assert.FileExists(t, filepath.Join(commandsDir, "keep.md"), "a project-unique command is still written")

	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	assert.NotContains(t, string(manifest), "recover.md", "a deduped command must not be manifest-tracked")
	assert.Contains(t, string(manifest), "keep.md")
}

// TestFileTemplateDelivery_DeliverCommands_DivergentHomeCopyWritesNormally
// verifies a same-named home file that differs from the rendered project bytes
// (version skew) is never silently hidden: the project copy is still written.
func TestFileTemplateDelivery_DeliverCommands_DivergentHomeCopyWritesNormally(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	old := agent.CommandExport{Name: "recover", Content: "OLD BODY", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, old)

	projectDir := t.TempDir()
	updated := agent.CommandExport{Name: "recover", Content: "NEW BODY", Enabled: true}

	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, nil)
	_, err := d.DeliverCommands([]agent.CommandExport{updated})
	require.NoError(t, err)

	commandsDir := filepath.Join(projectDir, ".claude", "commands")
	got, err := os.ReadFile(filepath.Join(commandsDir, "recover.md"))
	require.NoError(t, err)
	assert.Equal(t, TransformToClaudeCommand(updated), string(got), "a divergent home copy must not suppress the project write")

	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "recover.md")
}

// TestFileTemplateDelivery_DeliverCommands_DedupConvergence verifies the manifest
// reconcile: a file a PREVIOUS run delivered (and manifest-tracked) that this
// run's dedup now skips is not just left stale on disk — it is removed and
// dropped from the manifest, so the project scope actually converges to
// matching the global copy instead of leaving an orphaned duplicate.
func TestFileTemplateDelivery_DeliverCommands_DedupConvergence(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	// No home copy yet for this run.

	projectDir := t.TempDir()
	cmd := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, nil)

	// Run 1: delivered and manifest-tracked normally (no home copy to dedup against).
	_, err := d.DeliverCommands([]agent.CommandExport{cmd})
	require.NoError(t, err)
	commandsDir := filepath.Join(projectDir, ".claude", "commands")
	require.FileExists(t, filepath.Join(commandsDir, "recover.md"), "precondition: run 1 delivered the file")
	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	require.Contains(t, string(manifest), "recover.md", "precondition: run 1 manifest-tracked the file")

	// A byte-identical home copy now appears.
	writeRenderedHomeCommand(t, fakeHome, cmd)

	// Run 2: dedup now applies -> the stale project copy must be removed and
	// dropped from the manifest, not merely left un-rewritten.
	_, err = d.DeliverCommands([]agent.CommandExport{cmd})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(commandsDir, "recover.md"), "run 2 must remove the now-deduped stale project copy")
	if manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name)); err == nil {
		assert.NotContains(t, string(manifest), "recover.md", "run 2 must drop the deduped file from the manifest")
	}
}

// TestFileTemplateDelivery_DeliverCommands_HomeScopeDeliveryDisablesDedup
// verifies a directory never dedups against itself: when the delivery target
// IS the resolved global commands directory (workDir == $HOME), the file is
// still delivered even though it is byte-identical to "itself".
func TestFileTemplateDelivery_DeliverCommands_HomeScopeDeliveryDisablesDedup(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	cmd := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	// Pre-seed the exact file the delivery is about to (re)write, at the same
	// path DeliverCommands targets — this is the "identical to itself" case.
	writeRenderedHomeCommand(t, fakeHome, cmd)

	d := newFileTemplateDelivery(fakePlacement{dir: fakeHome}, nil)
	_, err := d.DeliverCommands([]agent.CommandExport{cmd})
	require.NoError(t, err)

	commandsDir := filepath.Join(fakeHome, ".claude", "commands")
	assert.FileExists(t, filepath.Join(commandsDir, "recover.md"), "a home-scope delivery must still write, never self-dedup")
	manifest, err := os.ReadFile(filepath.Join(commandsDir, ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "recover.md", "a home-scope delivery must still manifest-track its write")
}

// TestFileTemplateDelivery_DeliverCommands_SelfContainedSkipsHomeDedup verifies
// the materialize-only opt-out (sour-feed): `profile materialize --target` must
// produce a PORTABLE, self-contained tree, so a command that happens to be
// deduped against the DELIVERING machine's ~/.claude/commands must still land
// in the target when selfContainedCommands is set — the launch environment is
// not this host and would otherwise silently lose it. The default (false)
// behavior — dedup against home, as live launch/apply/container want — must be
// unchanged.
func TestFileTemplateDelivery_DeliverCommands_SelfContainedSkipsHomeDedup(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	dup := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, dup)

	// Default (false): unchanged — still dedups against home.
	projectDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: projectDir}, nil)
	_, err := d.DeliverCommands([]agent.CommandExport{dup})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(projectDir, ".claude", "commands", "recover.md"),
		"default (non-self-contained) delivery must still dedup against home")

	// selfContainedCommands = true: the portable target must keep the command even
	// though it shadows a command in the delivering machine's home.
	selfContainedDir := t.TempDir()
	sc := newFileTemplateDelivery(fakePlacement{dir: selfContainedDir}, nil)
	sc.selfContainedCommands = true
	_, err = sc.DeliverCommands([]agent.CommandExport{dup})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(selfContainedDir, ".claude", "commands", "recover.md"),
		"selfContainedCommands delivery must NOT dedup against the delivering machine's home — the portable target must keep every command")
}

// TestFileTemplateDelivery_DeliverSettings verifies the settings surface (hooks +
// statusline) is written into the injected Placement identically to
// writeSettingsFile, and that Cleanup reverts ctxloom hooks + managed statusline
// while preserving user-authored hooks. It also confirms DeliverSettings does
// not write .mcp.json (that is DeliverMCP's surface).
func TestFileTemplateDelivery_DeliverSettings(t *testing.T) {
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
		},
	}

	deliverDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: deliverDir}, nil)
	handle, err := d.DeliverSettings(hooks, true)
	require.NoError(t, err)

	// Control: the existing settings writer (manageStatusline true == default
	// statusLineDisabled false).
	controlDir := t.TempDir()
	require.NoError(t, (&ClaudeCodeHookWriter{}).writeSettingsFile(hooks, nil, controlDir))

	got, err := os.ReadFile(filepath.Join(deliverDir, ".claude", "settings.json"))
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join(controlDir, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "DeliverSettings must write the same settings.json as writeSettingsFile")

	// The settings surface must not create .mcp.json.
	assert.NoFileExists(t, filepath.Join(deliverDir, ".mcp.json"), "DeliverSettings must not write the MCP surface")

	// Cleanup removes ctxloom hooks and the managed statusline.
	require.NoError(t, handle.Cleanup())
	data, err := os.ReadFile(filepath.Join(deliverDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.NotContains(t, settings, "hooks", "ctxloom hooks must be reverted")
	assert.NotContains(t, settings, "statusLine", "managed statusline must be reverted")
}

// TestFileTemplateDelivery_DeliverSettings_PreservesUserHooks verifies a
// pre-existing user hook survives delivery and cleanup while the ctxloom hook is
// added and then reverted around it.
func TestFileTemplateDelivery_DeliverSettings_PreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	existing := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks":   []any{map[string]any{"type": "command", "command": "./user-hook.sh"}},
				},
			},
		},
		"otherSetting": "preserved",
	}
	data, err := json.Marshal(existing)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644))

	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}}},
	}
	d := newFileTemplateDelivery(fakePlacement{dir: dir}, nil)
	handle, err := d.DeliverSettings(hooks, true)
	require.NoError(t, err)

	require.NoError(t, handle.Cleanup())

	out, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(out, &settings))

	assert.Equal(t, "preserved", settings["otherSetting"], "unrelated settings must survive")
	hookMap, ok := settings["hooks"].(map[string]any)
	require.True(t, ok, "user hook must survive cleanup")
	require.Contains(t, hookMap, "PreToolUse")
	pre := hookMap["PreToolUse"].([]any)
	require.Len(t, pre, 1)
	m := pre[0].(map[string]any)
	inner := m["hooks"].([]any)
	require.Len(t, inner, 1)
	assert.Equal(t, "./user-hook.sh", inner[0].(map[string]any)["command"])
}
