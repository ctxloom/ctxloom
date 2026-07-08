package antigravity

import (
	"encoding/json"
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

// captureStderr redirects os.Stderr around fn and returns what was written (the
// Unsafe adapter's WARN streams there with no recorded finding).
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
		Context: "the secret color is vermilion",
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
				PreTool: []wire.Hook{{Command: "ltk evaluate"}},
			},
		},
		Skills: []agent.CommandExport{
			{Name: "review", Content: "Review the diff", Enabled: true, Description: "Code review"},
		},
	}
}

// ---- compile-time capability guarantees -----------------------------------
//
// Every agy surface is Delivery-ONLY (agy exposes no out-of-cwd redirect),
// proven by these assignments compiling with NO agent.RaceSafeDelivery line.
var (
	_ agent.Delivery = (*contextSurface)(nil)
	_ agent.Delivery = (*mcpSurface)(nil)
	_ agent.Delivery = (*hooksSurface)(nil)
	_ agent.Delivery = (*skillsSurface)(nil)
)

// Negative case — the compile-time guarantee, which cannot be asserted at
// runtime and so is documented here:
//
//	SharedCell{}.Deliver(surfaces.Hooks)   // COMPILE ERROR
//
// No agy surface implements agent.RaceSafeDelivery (agy has no per-invocation
// config redirect), so none is assignable to SharedCell.Deliver's
// RaceSafeDelivery parameter. The only way an agy surface reaches a shared cwd is
// the explicit, warned agent.Unsafe(…) — proven in TestUnsafe_WarnsAndProceeds.

func mcpServersOf(t *testing.T, fs afero.Fs, dir string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, filepath.Join(dir, AgentsDir, "mcp_config.json"))
	require.NoError(t, err)
	var m struct {
		Servers map[string]any `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &m))
	return m.Servers
}

// ---- context surface (.agents/AGENTS.md) -----------------------------------

func TestContextSurface_DeliverWritesAGENTSmd(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, filepath.Join(dir, AgentsDir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "the secret color is vermilion")
	assert.Contains(t, string(data), managedContextBegin, "context lands in the managed section")

	require.NoError(t, handle.Cleanup())
	exists, _ := afero.Exists(fs, filepath.Join(dir, AgentsDir, "AGENTS.md"))
	assert.False(t, exists, "cleanup strips the wholly-managed AGENTS.md")
}

// ---- MCP surface (.agents/mcp_config.json) ---------------------------------

func TestMCPSurface_DeliverWritesMCPConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.MCP.Deliver(dir)
	require.NoError(t, err)

	servers := mcpServersOf(t, fs, dir)
	assert.Contains(t, servers, AppMCPServerName)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")

	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, fs, dir)
	assert.NotContains(t, servers, AppMCPServerName, "cleanup reverts the managed servers")
}

// ---- hooks surface (.agents/hooks.json) ------------------------------------

func TestHooksSurface_DeliverWritesHooksJSON(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Hooks.Deliver(dir)
	require.NoError(t, err)

	path := filepath.Join(dir, AgentsDir, "hooks.json")
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ltk evaluate", "the managed hook is written")
	assert.Contains(t, string(data), "PreToolUse")

	require.NoError(t, handle.Cleanup())
	data, err = afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ltk evaluate", "cleanup reverts the managed hook")

	// The hooks surface never touches the MCP or context files.
	exists, _ := afero.Exists(fs, filepath.Join(dir, AgentsDir, "mcp_config.json"))
	assert.False(t, exists, "hooks surface never writes mcp_config.json")
	exists, _ = afero.Exists(fs, filepath.Join(dir, AgentsDir, "AGENTS.md"))
	assert.False(t, exists, "hooks surface never writes AGENTS.md")
}

// ---- skills surface (.agents/skills/) --------------------------------------

func TestSkillsSurface_DeliverWritesSkillFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	skillPath := filepath.Join(dir, AgentsDir, "skills", "review.md")
	exists, _ := afero.Exists(fs, skillPath)
	assert.True(t, exists, "the skill file lands under .agents/skills/")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, skillPath)
	assert.False(t, exists, "cleanup reverts the manifest-tracked skill")
}

// ---- Unsafe (the only path to a shared cwd) --------------------------------

func TestUnsafe_WarnsAndProceeds(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live-cwd"
	s := NewSurfaces(sampleInputs(), fs)

	rs := Unsafe(s.Skills, "skills", "antigravity has no out-of-cwd flag for .agents/skills/", cwd)

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
	exists, _ := afero.Exists(fs, filepath.Join(cwd, AgentsDir, "skills", "review.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd")
	require.NoError(t, handle.Cleanup())
}

// ---- cells wiring ----------------------------------------------------------

func TestDirectoryIsolatedCell_AcceptsAllAntigravitySurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/worktree"
	s := NewSurfaces(sampleInputs(), fs)

	ds := s.Deliveries()
	require.Len(t, ds, 4, "context, MCP, hooks, skills")

	cell := agent.NewDirectoryIsolatedCell(dir)
	for _, surface := range ds {
		d, err := cell.Deliver(surface)
		require.NoError(t, err)
		require.NotNil(t, d)
	}

	for _, rel := range []string{
		filepath.Join(AgentsDir, "AGENTS.md"),
		filepath.Join(AgentsDir, "mcp_config.json"),
		filepath.Join(AgentsDir, "hooks.json"),
		filepath.Join(AgentsDir, "skills", "review.md"),
	} {
		exists, _ := afero.Exists(fs, filepath.Join(dir, rel))
		assert.True(t, exists, "expected %s", rel)
	}
}

func TestSharedCell_AcceptsOnlyUnsafeAntigravitySurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live"
	s := NewSurfaces(sampleInputs(), fs)

	rs := s.UnsafeForSharedCwd(cwd)
	require.Len(t, rs, 4)
	stderr := captureStderr(t, func() {
		for _, surface := range rs {
			d, err := (agent.SharedCell{}).Deliver(surface)
			require.NoError(t, err)
			require.NotNil(t, d)
		}
	})
	assert.Contains(t, stderr, "warning:", "every agy surface warns when a SharedCell delivers it")
}
