package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookCommands returns every hook command present in settings.json, flattened
// across events and matchers. It reads the FILE rather than the writer's return
// value: this whole test pins what happens to bytes on a user's disk, and a
// writer that reported success while changing nothing is this project's
// characteristic failure.
func hookCommands(t *testing.T, projectDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &settings))
	var out []string
	for _, matchers := range settings.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				out = append(out, h.Command)
			}
		}
	}
	return out
}

// THE RULING (human, 2026-08-28): an EMPTY declared set means reconcile to
// empty — ctxloom retracts what it installed last round. This asserts the
// EFFECT ON DISK, not that the writer returned nil.
//
// The user's own hook is the control. Without it a passing test could not tell
// "retracted ctxloom's entries" from "wiped the file", and wiping the file is
// the outcome that would cost a user their configuration.
func TestClaudeCodeHookWriter_EmptyHookSet_RetractsOnlyWhatCtxloomInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	installed := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		PreTool: []wire.Hook{{Command: "./ctxloom-managed.sh", Matcher: "Bash"}},
	}}
	require.NoError(t, writer.WriteSettings(installed, ctxloomBundleMCP(), tmpDir))
	require.Contains(t, hookCommands(t, tmpDir), "./ctxloom-managed.sh",
		"precondition: the hook must actually be installed, or the retraction below proves nothing")

	// A hand-authored hook ctxloom never claimed. It must survive.
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(raw, &settings))
	hooks := settings["hooks"].(map[string]any)
	hooks["SessionStart"] = []any{map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": "./users-own.sh"}},
	}}
	out, err := json.Marshal(settings)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(settingsPath, out, 0o644))

	// Nothing configured this round.
	require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, nil, tmpDir))

	after := hookCommands(t, tmpDir)
	assert.NotContains(t, after, "./ctxloom-managed.sh",
		"an empty declared set must RETRACT the hook ctxloom installed last round")
	assert.Contains(t, after, "./users-own.sh",
		"retraction must not touch a hook ctxloom never claimed")
}
