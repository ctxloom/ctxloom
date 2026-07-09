package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextCacheHash returns the single hash under <work>/.ctxloom/cache/context.
func contextCacheHash(t *testing.T, work string) string {
	t.Helper()
	dir := filepath.Join(work, agent.SCMContextSubdir)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "the raw context cache dir must exist")
	require.Len(t, entries, 1, "exactly one context cache file")
	return strings.TrimSuffix(entries[0].Name(), ".md")
}

// TestCodex_Setup_DirectoryIsolated_ArtifactsAndHook drives codex Setup in an
// isolated cell and asserts the full cell package: the raw context cache file, the
// config.toml carrying the SessionStart inject-context hook KEYED TO the delivered
// context hash, the cell-scoped prompts under <work>/.codex/prompts, and the
// CODEX_HOME env pointing there.
func TestCodex_Setup_DirectoryIsolated_ArtifactsAndHook(t *testing.T) {
	work := t.TempDir()
	b := NewCodex()

	managed := &agent.ManagedConfig{
		Skills: []agent.CommandExport{{Name: "demo", Content: "do a thing", Enabled: true}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ctxloom hook guard", Type: "command"}},
		}},
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "run-srv"}}},
	}
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		CellKind:  agent.CellKindDirectoryIsolated,
		Managed:   managed,
	}))

	// The raw cache file is on disk (the SessionStart hook reads it at run time).
	hash := contextCacheHash(t, work)

	// config.toml carries the inject-context SessionStart hook keyed to that hash.
	cfg, err := os.ReadFile(filepath.Join(work, ".codex", "config.toml"))
	require.NoError(t, err, "the config surface must write .codex/config.toml")
	config := string(cfg)
	assert.Contains(t, config, "inject-context", "codex fires the SessionStart context-injection hook")
	assert.Contains(t, config, hash, "the injection hook is keyed to the delivered context hash")
	assert.Contains(t, config, "run-srv", "the managed MCP server is written to config.toml")

	// Cell-scoped prompts live under <work>/.codex/prompts (NOT the global ~/.codex).
	assert.FileExists(t, filepath.Join(work, ".codex", "prompts", "demo.md"),
		"skills are delivered to the cell-scoped prompts dir")

	// CODEX_HOME points codex at the cell-scoped home so it discovers those prompts.
	env := b.ExecuteEnv(&agent.ExecuteRequest{WorkDir: work, CellKind: agent.CellKindDirectoryIsolated})
	assert.Equal(t, filepath.Join(work, ".codex"), env["CODEX_HOME"],
		"an isolated cell scopes CODEX_HOME to <WorkDir>/.codex")
}

// TestCodex_CodexHomeEnv_AllCellsExceptSkipSetup proves CODEX_HOME is scoped to
// <WorkDir>/.codex in EVERY cell (including SharedCell, so codex finds the
// cell-scoped prompts in the live cwd), and is left unset for a minimal/distill
// (SkipSetup) run so codex keeps its global ~/.codex home.
func TestCodex_CodexHomeEnv_AllCellsExceptSkipSetup(t *testing.T) {
	b := NewCodex()
	work := "/proj"
	want := filepath.Join(work, ".codex")

	for _, cell := range []agent.CellKind{agent.CellKindShared, agent.CellKindDirectoryIsolated, agent.CellKindProcessIsolated} {
		env := b.ExecuteEnv(&agent.ExecuteRequest{WorkDir: work, CellKind: cell})
		assert.Equal(t, want, env["CODEX_HOME"], "CODEX_HOME is set for cell %v", cell)
	}

	env := b.ExecuteEnv(&agent.ExecuteRequest{WorkDir: work, CellKind: agent.CellKindShared, SkipSetup: true})
	_, ok := env["CODEX_HOME"]
	assert.False(t, ok, "a SkipSetup run keeps codex's global home (no CODEX_HOME override)")
}

// TestCodex_Setup_ConfigByteIdenticalToDirectWrite pins byte-identity: the
// config.toml the cell path's config surface writes is exactly what a direct
// MergeManaged + CodexHookWriter.WriteSettings on the merged state produces for
// the same workDir — same [hooks] (incl. the inject-context hook keyed to the
// same hash) and [mcp_servers]. Both run in the SAME workDir so the inject-context
// hook's --project path matches.
func TestCodex_Setup_ConfigByteIdenticalToDirectWrite(t *testing.T) {
	managed := &agent.ManagedConfig{
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ctxloom hook guard", Type: "command"}},
		}},
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "run-srv"}}},
	}
	fragments := []*agent.Fragment{{Content: "project rules"}}
	work := t.TempDir()
	configPath := filepath.Join(work, ".codex", "config.toml")

	// New path: the cell delivery writes config.toml via the config surface.
	b := NewCodex()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Fragments: fragments,
		CellKind:  agent.CellKindDirectoryIsolated,
		Managed:   managed,
	}))
	newCfg, err := os.ReadFile(configPath)
	require.NoError(t, err)
	hash := contextCacheHash(t, work)

	// Direct path in the SAME dir: delete the config surface's file, then reproduce
	// it via a fresh BaseLifecycle merge + CodexHookWriter.WriteSettings on the
	// merged hooks/MCP, keyed to the same hash — the write the config surface wraps.
	require.NoError(t, os.Remove(configPath))
	life := agent.NewBaseLifecycle("codex")
	life.MergeManaged(managed, work, hash)
	require.NoError(t, (&CodexHookWriter{}).WriteSettings(life.GetHooks(), life.GetMCP(), nil, work))
	directCfg, err := os.ReadFile(configPath)
	require.NoError(t, err)

	assert.Equal(t, string(directCfg), string(newCfg),
		"the cell path's config.toml is byte-identical to a direct merged-state write")
}
