package kiro

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
// Every kiro surface is Delivery-ONLY at this layer (the --agent name lever is a
// Phase-2 launch concern), proven by these assignments compiling with NO
// agent.RaceSafeDelivery line.
var (
	_ agent.Delivery = (*contextSurface)(nil)
	_ agent.Delivery = (*mcpSurface)(nil)
	_ agent.Delivery = (*settingsSurface)(nil)
	_ agent.Delivery = (*skillsSurface)(nil)
)

// Negative case — the compile-time guarantee, documented since it cannot be
// asserted at runtime:
//
//	SharedCell{}.Deliver(surfaces.Settings)   // COMPILE ERROR
//
// No kiro surface implements agent.RaceSafeDelivery, so none is assignable to
// SharedCell.Deliver's RaceSafeDelivery parameter. The only way a kiro surface
// reaches a shared cwd is the explicit, warned agent.Unsafe(…) — proven in
// TestUnsafe_WarnsAndProceeds.

func mcpServersOf(t *testing.T, fs afero.Fs, dir string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, filepath.Join(dir, kiroDir, "settings", "mcp.json"))
	require.NoError(t, err)
	var m struct {
		Servers map[string]any `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &m))
	return m.Servers
}

// ---- context surface (.kiro/steering/ctxloom-context.md) -------------------

func TestContextSurface_DeliverWritesSteering(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, filepath.Join(dir, kiroDir, "steering", steeringFileName))
	require.NoError(t, err)
	assert.Contains(t, string(data), "the secret color is vermilion")
	assert.Contains(t, string(data), "inclusion: always", "steering carries the auto-load front-matter")

	require.NoError(t, handle.Cleanup())
	exists, _ := afero.Exists(fs, filepath.Join(dir, kiroDir, "steering", steeringFileName))
	assert.False(t, exists, "cleanup removes the ctxloom-owned steering file")
}

// ---- MCP surface (.kiro/settings/mcp.json) ---------------------------------

func TestMCPSurface_DeliverWritesMCPJSON(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.MCP.Deliver(dir)
	require.NoError(t, err)

	servers := mcpServersOf(t, fs, dir)
	assert.Contains(t, servers, agent.MCPServerName)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")

	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, fs, dir)
	assert.NotContains(t, servers, agent.MCPServerName, "cleanup reverts the managed servers")
}

// ---- settings surface (.kiro/agents/<name>.json) ---------------------------

func TestSettingsSurface_DeliverWritesAgentConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Settings.Deliver(dir)
	require.NoError(t, err)

	path := filepath.Join(dir, kiroDir, "agents", defaultAgentName+".json")
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var a kiroAgent
	require.NoError(t, json.Unmarshal(data, &a))
	assert.Equal(t, defaultAgentName, a.Name)
	require.NotNil(t, a.Hooks, "the mapped hooks land in the agent JSON")
	require.Len(t, a.Hooks.PreToolUse, 1)
	assert.Equal(t, "ltk evaluate", a.Hooks.PreToolUse[0].Command)

	// The settings surface never touches steering or mcp.json.
	exists, _ := afero.Exists(fs, filepath.Join(dir, kiroDir, "steering", steeringFileName))
	assert.False(t, exists, "settings surface never writes steering")
	exists, _ = afero.Exists(fs, filepath.Join(dir, kiroDir, "settings", "mcp.json"))
	assert.False(t, exists, "settings surface never writes mcp.json")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, path)
	assert.False(t, exists, "cleanup removes the ctxloom-owned agent config")
}

// ---- skills surface (.kiro/skills/) ----------------------------------------

func TestSkillsSurface_DeliverWritesSkillMd(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	skillPath := filepath.Join(dir, kiroDir, "skills", "review", "SKILL.md")
	exists, _ := afero.Exists(fs, skillPath)
	assert.True(t, exists, "the SKILL.md lands under .kiro/skills/<name>/")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, skillPath)
	assert.False(t, exists, "cleanup reverts the manifest-tracked skill")
}

// ---- Unsafe (the only path to a shared cwd) --------------------------------

func TestUnsafe_WarnsAndProceeds(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live-cwd"
	s := NewSurfaces(sampleInputs(), fs)

	rs := Unsafe(s.Skills, "skills", "kiro has no out-of-cwd flag for .kiro/skills/", cwd)

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
	exists, _ := afero.Exists(fs, filepath.Join(cwd, kiroDir, "skills", "review", "SKILL.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd")
	require.NoError(t, handle.Cleanup())
}

// ---- cells wiring ----------------------------------------------------------

func TestDirectoryIsolatedCell_AcceptsAllKiroSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/worktree"
	s := NewSurfaces(sampleInputs(), fs)

	ds := s.Deliveries()
	require.Len(t, ds, 4, "context, MCP, settings, skills")

	cell := agent.NewDirectoryIsolatedCell(dir)
	for _, surface := range ds {
		d, err := cell.Deliver(surface)
		require.NoError(t, err)
		require.NotNil(t, d)
	}

	for _, rel := range []string{
		filepath.Join(kiroDir, "steering", steeringFileName),
		filepath.Join(kiroDir, "settings", "mcp.json"),
		filepath.Join(kiroDir, "agents", defaultAgentName+".json"),
		filepath.Join(kiroDir, "skills", "review", "SKILL.md"),
	} {
		exists, _ := afero.Exists(fs, filepath.Join(dir, rel))
		assert.True(t, exists, "expected %s", rel)
	}
}

func TestSharedCell_AcceptsOnlyUnsafeKiroSurfaces(t *testing.T) {
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
	assert.Contains(t, stderr, "warning:", "every kiro surface warns when a SharedCell delivers it")
}
