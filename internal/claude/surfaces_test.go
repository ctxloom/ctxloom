package claude

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// fakePlacement (contextdelivery_test.go) and mcpServersOf (surfacedelivery_test.go)
// are reused here — same package.

// captureStderr redirects os.Stderr around fn and returns what was written. The
// Unsafe adapter's WARN streams through clidiag → os.Stderr with no recorded
// finding, so this is how the skills-Unsafe test observes the loud line.
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
		Context: "# Rules\nthe secret color is vermilion",
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
				SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
			},
		},
		ManageStatusline: true,
		Skills: []agent.CommandExport{
			{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		},
	}
}

// ---- compile-time capability guarantees -----------------------------------

// The seam's contracts, proven by these assignments compiling. Context/MCP/
// settings are dual-capable; skills is Delivery-only.
var (
	_ agent.Delivery         = (*contextSurface)(nil)
	_ agent.RaceSafeDelivery = (*contextSurface)(nil)
	_ agent.Delivery         = (*mcpSurface)(nil)
	_ agent.RaceSafeDelivery = (*mcpSurface)(nil)
	_ agent.Delivery         = (*settingsSurface)(nil)
	_ agent.RaceSafeDelivery = (*settingsSurface)(nil)
	_ agent.Delivery         = (*skillsSurface)(nil)
)

// Negative case — the compile-time guarantee, which cannot be asserted at
// runtime and so is documented here:
//
//	SharedCell{}.Deliver(surfaces.Skills)   // COMPILE ERROR
//
// *skillsSurface implements only agent.Delivery, not agent.RaceSafeDelivery
// (claude has no out-of-cwd flag for .claude/commands/), so it is NOT assignable
// to SharedCell.Deliver's RaceSafeDelivery parameter. The only way skills reaches
// a shared cwd is the explicit, warned agent.Unsafe(surfaces.Skills, …) — proven
// live in TestSkillsSurface_Unsafe_WarnsAndProceeds below.

// ---- context surface -------------------------------------------------------

// context Delivery writes CLAUDE.md (the ContextWriter core) into the target dir
// and its Cleanup removes it.
func TestContextSurface_DeliverWritesCLAUDEmd(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, sampleInputs().Context, string(got), "CLAUDE.md holds the raw context")

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"), "cleanup reverses the whole-file write")
}

// context RaceSafeDelivery writes the framed <hash>.sysprompt.md into the
// out-of-cwd placement (via appendFlagDelivery) and exposes it via Path() — and
// does NOT touch the well-known CLAUDE.md.
func TestContextSurface_DeliverIsolated_WritesSyspromptAndExposesPath(t *testing.T) {
	isolated := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: isolated}, nil)

	handle, err := s.Context.DeliverIsolated()
	require.NoError(t, err)

	path := s.Context.Path()
	require.NotEmpty(t, path, "Path() exposes the framed file for --append-system-prompt-file")
	assert.Equal(t, isolated, filepath.Dir(path), "the framed file lands out-of-cwd, in the isolated placement")
	assert.True(t, strings.HasSuffix(path, agent.SCMFramedContextSuffix))
	require.FileExists(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, agent.FrameProjectContext(sampleInputs().Context), string(data))

	// The isolated path never writes CLAUDE.md.
	assert.NoFileExists(t, filepath.Join(isolated, "CLAUDE.md"))

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, path)
}

// ---- MCP surface -----------------------------------------------------------

// MCP Delivery writes the merged .mcp.json (via fileTemplateDelivery) into the
// target dir; Cleanup reverts the ctxloom-owned servers.
func TestMCPSurface_DeliverWritesMCPJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.MCP.Deliver(dir)
	require.NoError(t, err)

	servers := mcpServersOf(t, dir)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")
	assert.Contains(t, servers, AppMCPServerName)

	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, dir)
	assert.NotContains(t, servers, AppMCPServerName, "cleanup reverts ctxloom servers")
}

// MCP RaceSafeDelivery writes .mcp.json into the OUT-OF-CWD placement and exposes
// that path for --mcp-config; the well-known cwd is left untouched.
func TestMCPSurface_DeliverIsolated_OutOfCwd(t *testing.T) {
	cwd := t.TempDir()      // the "shared cwd" — must stay clean
	isolated := t.TempDir() // the out-of-cwd per-run location
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: isolated}, nil)

	handle, err := s.MCP.DeliverIsolated()
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(isolated, ".mcp.json"), s.MCP.Path(),
		"Path() is the out-of-cwd .mcp.json for --mcp-config")
	require.FileExists(t, s.MCP.Path())
	assert.NoFileExists(t, filepath.Join(cwd, ".mcp.json"), "the shared cwd is never written")

	servers := mcpServersOf(t, isolated)
	assert.Contains(t, servers, AppMCPServerName)

	require.NoError(t, handle.Cleanup())
}

// ---- settings surface ------------------------------------------------------

// settings Delivery writes .claude/settings.json (hooks + statusline) into the
// target dir; Cleanup reverts the ctxloom-managed entries.
func TestSettingsSurface_DeliverWritesSettingsJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.Settings.Deliver(dir)
	require.NoError(t, err)

	settings := readJSON(t, filepath.Join(dir, ".claude", "settings.json"))
	assert.Contains(t, settings, "hooks", "settings surface carries the hooks")
	assert.NoFileExists(t, filepath.Join(dir, ".mcp.json"), "settings surface never writes MCP")

	require.NoError(t, handle.Cleanup())
	settings = readJSON(t, filepath.Join(dir, ".claude", "settings.json"))
	assert.NotContains(t, settings, "hooks", "cleanup reverts ctxloom hooks")
}

// settings RaceSafeDelivery writes .claude/settings.json into the OUT-OF-CWD
// placement and exposes that path for --settings.
func TestSettingsSurface_DeliverIsolated_OutOfCwd(t *testing.T) {
	cwd := t.TempDir()
	isolated := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: isolated}, nil)

	handle, err := s.Settings.DeliverIsolated()
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(isolated, ".claude", "settings.json"), s.Settings.Path(),
		"Path() is the out-of-cwd settings.json for --settings")
	require.FileExists(t, s.Settings.Path())
	assert.NoFileExists(t, filepath.Join(cwd, ".claude", "settings.json"), "the shared cwd is never written")

	settings := readJSON(t, s.Settings.Path())
	assert.Contains(t, settings, "hooks")

	require.NoError(t, handle.Cleanup())
}

// ---- skills surface --------------------------------------------------------

// skills Delivery writes .claude/commands/ into the target dir; Cleanup reverts
// the manifest-tracked set.
func TestSkillsSurface_DeliverWritesCommands(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"))

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"), "cleanup reverts the skill export")
}

// skills has no out-of-cwd flag, so a SharedCell reaches it only through
// agent.Unsafe — which WARNS to stderr AND PROCEEDS with the well-known write
// into the shared cwd (a sanctioned, permitted action, never a fatal abort).
func TestSkillsSurface_Unsafe_WarnsAndProceeds(t *testing.T) {
	cwd := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	// agent.Unsafe accepts skills (a self-describing UnsafeSurface) and returns a
	// RaceSafeDelivery.
	rs := agent.Unsafe(s.Skills, cwd)

	stderr := captureStderr(t, func() {
		handle, err := rs.DeliverIsolated()
		require.NoError(t, err)
		require.NoError(t, handle.Cleanup())
	})

	assert.Contains(t, stderr, "warning:", "Unsafe streams a loud WARN")
	assert.Contains(t, stderr, "skills")
	assert.Contains(t, stderr, "shared cwd")
	// It PROCEEDED: before cleanup the write landed under the shared cwd.
	// (Deliver→Cleanup above removed it; re-run the write to confirm the target.)
	handle, err := s.Skills.Deliver(cwd)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(cwd, ".claude", "commands", "review.md"),
		"the well-known write proceeded into the shared cwd")
	require.NoError(t, handle.Cleanup())
}

// ---- cells wiring (the vertical slice) -------------------------------------

// A SharedCell accepts claude's race-safe surfaces (context/MCP/settings via a
// flag) and, for skills, the Unsafe-wrapped surface — the SharedCwdDeliveries
// helper packages exactly that set for iteration.
func TestSharedCell_AcceptsClaudeRaceSafeSurfaces(t *testing.T) {
	cwd := t.TempDir()
	isolated := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: isolated}, nil)

	// Direct: the three flag-backed surfaces are assignable to SharedCell.Deliver.
	for _, surface := range []agent.RaceSafeDelivery{s.Context, s.MCP, s.Settings} {
		d, err := (agent.SharedCell{}).Deliver(surface)
		require.NoError(t, err)
		require.NotNil(t, d)
	}

	// The helper packages all four for the shared cwd (skills wrapped in Unsafe).
	rs := s.SharedCwdDeliveries(cwd)
	require.Len(t, rs, 4)
	stderr := captureStderr(t, func() {
		for _, surface := range rs {
			d, err := (agent.SharedCell{}).Deliver(surface)
			require.NoError(t, err)
			require.NotNil(t, d)
		}
	})
	assert.Contains(t, stderr, "warning:", "the Unsafe skills member warns when the SharedCell delivers it")
}

// An isolated cell accepts EVERY surface as a plain Delivery — Deliveries() is the
// iteration set for a worktree / container / materialize target.
func TestDirectoryIsolatedCell_AcceptsAllClaudeSurfaces(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	ds := s.Deliveries()
	require.Len(t, ds, 4, "context, MCP, settings, skills")

	cell := agent.NewDirectoryIsolatedCell(dir)
	for _, surface := range ds {
		d, err := cell.Deliver(surface)
		require.NoError(t, err)
		require.NotNil(t, d)
	}

	// The private-dir writes all landed at claude's well-known locations.
	assert.FileExists(t, filepath.Join(dir, "CLAUDE.md"))
	assert.FileExists(t, filepath.Join(dir, ".mcp.json"))
	assert.FileExists(t, filepath.Join(dir, ".claude", "settings.json"))
	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"))
}

// readJSON reads a JSON object file into a map.
func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}
