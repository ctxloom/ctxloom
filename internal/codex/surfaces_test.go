package codex

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// captureStderr redirects os.Stderr around fn and returns what was written. The
// Unsafe adapter's WARN streams through clidiag → os.Stderr with no recorded
// finding, so this is how the Unsafe test observes the loud line.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// sampleInputs is a representative, fully-populated SurfaceInputs.
func sampleInputs() SurfaceInputs {
	return SurfaceInputs{
		Fragments: []*agent.Fragment{{Name: "rules", Content: "the secret color is vermilion"}},
		MCP: &wire.MCPConfig{
			Servers: map[string]wire.MCPServer{
				"config-server": {Command: "config-cmd", Args: []string{"--flag"}},
			},
		},
		BundleMCP: map[string]wire.MCPServer{
			"bundle-server": {Command: "bundle-cmd", SCM: "ctxloom-bundle:test"},
		},
		Hooks: &wire.HooksConfig{
			Unified: wire.UnifiedHooks{
				// A ctxloom-managed command (recognized by codex's removeManagedHooks
				// on cleanup, which keys on the ctxloom executable token — a companion
				// hook like "ltk evaluate" carries no marker and would persist).
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-start"}},
			},
		},
		Skills: []agent.CommandExport{
			{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		},
	}
}

// ---- compile-time capability guarantees -----------------------------------
//
// Every codex surface is Delivery-ONLY (codex exposes no out-of-cwd redirect),
// proven by these assignments compiling with NO agent.RaceSafeDelivery line.
var (
	_ agent.Delivery = (*contextSurface)(nil)
	_ agent.Delivery = (*configSurface)(nil)
	_ agent.Delivery = (*agent.ManagedSkillsDelivery)(nil) // codex's prompts surface
)

// Negative case — the compile-time guarantee, which cannot be asserted at
// runtime and so is documented here:
//
//	SharedCell{}.Deliver(surfaces.Skills)   // COMPILE ERROR
//	SharedCell{}.Deliver(surfaces.Config)   // COMPILE ERROR
//
// No codex surface implements agent.RaceSafeDelivery (codex has no
// --mcp-config / --settings / --append-system-prompt equivalent), so none is
// assignable to SharedCell.Deliver's RaceSafeDelivery parameter. The only way a
// codex surface reaches a shared cwd is the explicit, warned agent.Unsafe(…) —
// proven live in TestUnsafe_WarnsAndProceeds below.

// contextFile returns the single .md context file WriteContextFile wrote under
// dir's context cache (there is exactly one for one delivery).
func contextFile(t *testing.T, fs afero.Fs, dir string) string {
	t.Helper()
	cacheDir := filepath.Join(dir, agent.SCMContextSubdir)
	entries, err := afero.ReadDir(fs, cacheDir)
	require.NoError(t, err)
	var md []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			md = append(md, filepath.Join(cacheDir, e.Name()))
		}
	}
	require.Len(t, md, 1, "exactly one context file")
	return md[0]
}

// ---- context surface -------------------------------------------------------

// context Delivery writes the raw context file (via agent.WriteContextFile) into
// dir's well-known cache and its Cleanup removes it.
func TestContextSurface_DeliverWritesContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)

	path := contextFile(t, fs, dir)
	got, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, "the secret color is vermilion", string(got), "the context file holds the assembled context")

	require.NoError(t, handle.Cleanup())
	exists, _ := afero.Exists(fs, path)
	assert.False(t, exists, "cleanup reverses the context-file write")
}

// Empty context writes nothing and cleans up to a no-op.
func TestContextSurface_EmptyContextIsNoOp(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewSurfaces(SurfaceInputs{}, fs)

	handle, err := s.Context.Deliver("/proj")
	require.NoError(t, err)
	require.NoError(t, handle.Cleanup())

	exists, _ := afero.Exists(fs, filepath.Join("/proj", agent.SCMContextSubdir))
	assert.False(t, exists, "no context, nothing written")
}

// ---- config surface (folded settings + hooks + MCP) ------------------------

// config Delivery writes .codex/config.toml (hooks + mcp_servers) into dir via
// the reused WriteSettings; Cleanup reverts the ctxloom-managed entries.
func TestConfigSurface_DeliverWritesConfigTOML(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Config.Deliver(dir)
	require.NoError(t, err)

	cfgPath := filepath.Join(dir, ".codex", "config.toml")
	cfg := readConfig(t, fs, cfgPath)
	servers := asMap(cfg["mcp_servers"])
	require.NotNil(t, servers, "config surface carries the MCP servers")
	assert.Contains(t, servers, agent.MCPServerName)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")
	assert.Contains(t, cfg, "hooks", "config surface carries the hooks (folded into config.toml)")

	require.NoError(t, handle.Cleanup())
	cfg = readConfig(t, fs, cfgPath)
	assert.NotContains(t, asMap(cfg["mcp_servers"]), agent.MCPServerName, "cleanup reverts the managed ctxloom server")
	assert.NotContains(t, cfg, "hooks", "cleanup reverts the managed hooks")
}

// ---- skills surface (cell-scoped $CODEX_HOME) ------------------------------

// skills Delivery writes prompts under the CELL-SCOPED $CODEX_HOME derived from
// dir (<dir>/.codex/prompts) — NOT the global ~/.codex/prompts — so a
// DirectoryIsolatedCell isolates them. Cleanup reverts the manifest-tracked set.
func TestSkillsSurface_DeliverWritesCellScopedPrompts(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	promptPath := filepath.Join(dir, ".codex", "prompts", "review.md")
	exists, _ := afero.Exists(fs, promptPath)
	assert.True(t, exists, "the prompt lands under the cell-scoped $CODEX_HOME (<dir>/.codex/prompts)")
	assert.Equal(t, filepath.Join(dir, ".codex", "prompts"), cellScopedPromptsDir(dir))
	assert.Equal(t, filepath.Join(dir, ".codex"), cellScopedCodexHome(dir),
		"CODEX_HOME is <dir>/.codex, so the launched codex reads these prompts")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, promptPath)
	assert.False(t, exists, "cleanup reverts the manifest-tracked prompt")
}

// ---- Unsafe (the only path to a shared cwd) --------------------------------

// A codex surface has no out-of-cwd flag, so a SharedCell reaches it only through
// agent.Unsafe — which WARNS to stderr AND PROCEEDS with the well-known write
// into the shared cwd (a sanctioned, permitted action, never a fatal abort).
func TestUnsafe_WarnsAndProceeds(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live-cwd"
	s := NewSurfaces(sampleInputs(), fs)

	rs := agent.Unsafe(s.Skills, cwd)

	stderr := captureStderr(t, func() {
		handle, err := rs.DeliverIsolated()
		require.NoError(t, err)
		require.NoError(t, handle.Cleanup())
	})

	assert.Contains(t, stderr, "warning:", "Unsafe streams a loud WARN")
	assert.Contains(t, stderr, "skills")
	assert.Contains(t, stderr, "shared cwd")

	// It PROCEEDED: the well-known write lands under the shared cwd.
	handle, err := s.Skills.Deliver(cwd)
	require.NoError(t, err)
	exists, _ := afero.Exists(fs, filepath.Join(cwd, ".codex", "prompts", "review.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd")
	require.NoError(t, handle.Cleanup())
}

// ---- cells wiring ----------------------------------------------------------

// An isolated cell accepts EVERY codex surface as a plain Delivery — Deliveries()
// is the iteration set for a worktree / container / materialize target, and the
// only cell type codex's Delivery-only surfaces can enter directly.
func TestDirectoryIsolatedCell_AcceptsAllCodexSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/worktree"
	s := NewSurfaces(sampleInputs(), fs)

	ds := s.Deliveries()
	require.Len(t, ds, 3, "context, config, skills")

	cell := agent.NewDirectoryIsolatedCell(dir)
	for _, surface := range ds {
		d, err := cell.Deliver(surface)
		require.NoError(t, err)
		require.NotNil(t, d)
	}

	// The private-dir writes all landed at codex's well-known cell-local locations.
	contextFile(t, fs, dir) // asserts exactly one context file exists in the cell
	exists, _ := afero.Exists(fs, filepath.Join(dir, ".codex", "config.toml"))
	assert.True(t, exists)
	exists, _ = afero.Exists(fs, filepath.Join(dir, ".codex", "prompts", "review.md"))
	assert.True(t, exists)
}

// The SharedCell packages every codex surface through Unsafe (codex has no
// race-safe surface), and each warns when the cell delivers it.
func TestSharedCell_AcceptsOnlyUnsafeCodexSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live"
	s := NewSurfaces(sampleInputs(), fs)

	rs := s.SharedCwdDeliveries(cwd)
	require.Len(t, rs, 3)
	stderr := captureStderr(t, func() {
		for _, surface := range rs {
			d, err := (agent.SharedCell{}).Deliver(surface)
			require.NoError(t, err)
			require.NotNil(t, d)
		}
	})
	assert.Contains(t, stderr, "warning:", "every codex surface warns when a SharedCell delivers it")
}
