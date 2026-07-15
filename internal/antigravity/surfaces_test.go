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
func sampleInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
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
		Commands: []agent.CommandExport{
			{Name: "review", Content: "Review the diff", Enabled: true, Description: "Code review"},
		},
		Skills: []agent.SkillExport{
			{
				Name:        "humanize",
				Description: "Removes AI writing tells",
				Enabled:     true,
				Files: []agent.PackageFile{
					{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: Removes AI writing tells\n---\n\nBody.\n"), Mode: 0644},
					{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0755},
				},
			},
			{Name: "disabled-skill", Enabled: false, Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("nope")}}},
		},
	}
}

// ---- compile-time capability guarantees -----------------------------------
//
// Every agy surface is Delivery-ONLY (agy exposes no out-of-cwd redirect and no
// SharedRealization), proven by these assignments compiling.
var (
	_ agent.Delivery = (*contextSurface)(nil)
	_ agent.Delivery = (*mcpSurface)(nil)
	_ agent.Delivery = (*hooksSurface)(nil)
	_ agent.Delivery = (*agent.ManagedCommandsDelivery)(nil) // agy's commands surface
)

// No agy surface has a SharedRealization (agy has no per-invocation config
// redirect), so a SHARED-cwd delivery of any agy surface always falls back to the
// loud well-known write — proven in TestUnsafe_WarnsAndProceeds.

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

// ---- commands surface (.agents/skills/) ------------------------------------

func TestCommandsSurface_DeliverWritesSkillFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Commands.Deliver(dir)
	require.NoError(t, err)

	// G3: commands render as `<name>/SKILL.md` DIRECTORIES (the shape agy's
	// skill scanner actually discovers), not the old flat `<name>.md`.
	commandPath := filepath.Join(dir, AgentsDir, "skills", "review", "SKILL.md")
	exists, _ := afero.Exists(fs, commandPath)
	assert.True(t, exists, "the command file lands under .agents/skills/<name>/SKILL.md")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, commandPath)
	assert.False(t, exists, "cleanup reverts the manifest-tracked command")
}

// ---- skills surface (.agents/skills/<name>/SKILL.md) -----------------------

// skills Delivery writes .agents/skills/<name>/SKILL.md (+ sibling files) into
// the target dir with the exec bit preserved; a disabled skill is not written;
// Cleanup reverts the manifest-tracked set. Post-G3 the commands surface
// renders the SAME `<name>/SKILL.md` shape (capabilities.go), so a same-name
// command and skill now collide on the identical path — resolved skill-wins
// by the shared agent.FilterCommandsClaimedBySkills
// (TestClash_SkillWinsOverSameNamedCommand below),
// not by the two surfaces writing distinct filesystem entries as before.
func TestSkillsSurface_DeliverWritesSkillFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	skillMD := filepath.Join(dir, AgentsDir, "skills", "humanize", "SKILL.md")
	exists, _ := afero.Exists(fs, skillMD)
	assert.True(t, exists, "skill lands under .agents/skills/<name>/SKILL.md")

	data, err := afero.ReadFile(fs, skillMD)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Body.")

	scriptPath := filepath.Join(dir, AgentsDir, "skills", "humanize", "scripts", "run.sh")
	info, err := fs.Stat(scriptPath)
	require.NoError(t, err, "scripts/run.sh must be materialized")
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit survives agy's skills surface")

	exists, _ = afero.Exists(fs, filepath.Join(dir, AgentsDir, "skills", "disabled-skill", "SKILL.md"))
	assert.False(t, exists, "a skill with Enabled == false must not be written")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, skillMD)
	assert.False(t, exists, "cleanup reverts the manifest-tracked skill")
}

// ---- no SharedRealization (the only path to a shared cwd) ------------------

func TestUnsafe_WarnsAndProceeds(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live-cwd"
	s := NewSurfaces(sampleInputs(), fs)

	r, err := agent.Select(s).WithCommands(agent.CommandsWriteUnsafeFile).Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 1)

	assert.Contains(t, stderr, "warning:", "the fallback streams a loud WARN")
	assert.Contains(t, stderr, "commands")
	assert.Contains(t, stderr, "shared cwd")

	// It PROCEEDED: the well-known write lands under the shared cwd.
	exists, _ := afero.Exists(fs, filepath.Join(cwd, AgentsDir, "skills", "review", "SKILL.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd")
	require.NoError(t, delivered[0].Cleanup())
}

// ---- cells wiring ----------------------------------------------------------

func TestDirectoryIsolatedCell_AcceptsAllAntigravitySurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/worktree"
	s := NewSurfaces(sampleInputs(), fs)

	ds := s.Deliveries()
	require.Len(t, ds, 5, "context, MCP, hooks, commands, skills")

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
		filepath.Join(AgentsDir, "skills", "review", "SKILL.md"),
		filepath.Join(AgentsDir, "skills", "humanize", "SKILL.md"),
	} {
		exists, _ := afero.Exists(fs, filepath.Join(dir, rel))
		assert.True(t, exists, "expected %s", rel)
	}
}

func TestSharedCell_AcceptsOnlyUnsafeAntigravitySurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	cwd := "/live"
	s := NewSurfaces(sampleInputs(), fs)

	r, err := agent.Select(s).WithEverything().Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 5)
	assert.Contains(t, stderr, "warning:", "every agy surface warns when DeliverShared delivers it")
}

// ---- approach dispatch (vital-tiger v2) -------------------------------------

// SupportedApproaches pins agy's per-surface table: every surface is
// native-file-only — agy has no out-of-cwd flag and no SessionStart hook.
func TestSurfaces_SupportedApproaches(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(kind), "%s", kind)
	}
}

// DefaultApproach is the native file for every kind agy has.
func TestSurfaces_DefaultApproach(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		a, ok := s.DefaultApproach(kind)
		require.True(t, ok, "%s", kind)
		assert.Equal(t, agent.ApproachUnsafeFile, a)
	}
}

// A non-UnsafeFile approach is unsupported for every agy surface (no hook, no flag).
func TestSurfaceFor_UnsupportedApproachErrors(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	_, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachHook)
	assert.Error(t, err, "agy reads AGENTS.md directly — no SessionStart hook")
}

// SharedRealization is absent for every kind: agy has no out-of-cwd redirect, so a
// SHARED-cwd delivery always falls back to the loud well-known write.
func TestSurfaces_SharedRealization_Absent(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		_, ok := s.SharedRealization(kind)
		assert.False(t, ok, "%s: agy has no out-of-cwd realization", kind)
	}
}

// ---- G3 command/skill name-collision guard (mirrors kiro's D6 tests) ------

// sampleAntigravitySkill builds a minimal enabled SkillExport with SKILL.md
// content distinguishable from a command's rendered content, for the clash
// tests below.
func sampleAntigravitySkill(name, body string) agent.SkillExport {
	return agent.SkillExport{
		Name:        name,
		Description: "skill: " + name,
		Enabled:     true,
		Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte(body), Mode: 0644},
			{RelPath: "assets/note.txt", Content: []byte("sibling file"), Mode: 0644},
		},
	}
}

// TestClash_SkillWinsOverSameNamedCommand is the G3 crux test: a command
// "gamma" and a skill "gamma" collide on the identical path
// .agents/skills/gamma/SKILL.md (both surfaces now emit `<name>/SKILL.md`
// dirs post-G3-fix). Materializing agy's full surface set must leave the
// SKILL's content (and its sibling files) at that path, never the command's,
// and the command's manifest (antigravityManifest) must not claim the path
// the skill wrote — no double-ownership, proven by cleaning up the COMMANDS
// surface alone and observing the skill's file survives untouched.
func TestClash_SkillWinsOverSameNamedCommand(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	in := agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "gamma", Content: "COMMAND VERSION", Enabled: true}},
		Skills:   []agent.SkillExport{sampleAntigravitySkill("gamma", "SKILL VERSION")},
	}
	s := NewSurfaces(in, fs)

	// Deliver in the same order Deliveries() uses: commands then skills.
	cmdHandle, err := s.Commands.Deliver(dir)
	require.NoError(t, err)
	skillHandle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	path := filepath.Join(dir, ".agents", "skills", "gamma", "SKILL.md")
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, "SKILL VERSION", string(data), "the skill package wins; the command's content must not be present")

	exists, _ := afero.Exists(fs, filepath.Join(dir, ".agents", "skills", "gamma", "assets", "note.txt"))
	assert.True(t, exists, "the skill's sibling files are present too")

	// The commands writer never saw "gamma" (agent.FilterCommandsClaimedBySkills dropped it
	// before WriteCommandFiles ran), so its manifest must not list the path
	// the skill owns — proven by cleaning up ONLY the commands surface and
	// observing the skill's file is untouched.
	require.NoError(t, cmdHandle.Cleanup())
	data, err = afero.ReadFile(fs, path)
	require.NoError(t, err, "the commands cleanup must not have deleted the skill's file")
	assert.Equal(t, "SKILL VERSION", string(data), "no double-ownership: commands cleanup left the skill's winning file intact")

	require.NoError(t, skillHandle.Cleanup())
	exists, _ = afero.Exists(fs, path)
	assert.False(t, exists, "skills cleanup removes the file it owns")
}

// TestClash_DisabledSkillDoesNotShadowCommand proves the collision rule is
// scoped to skills ENABLED for agy: a skill named "delta" disabled for this
// engine must not claim the name, so the command "delta" still writes its
// SKILL.md.
func TestClash_DisabledSkillDoesNotShadowCommand(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	disabledSkill := sampleAntigravitySkill("delta", "SKILL VERSION")
	disabledSkill.Enabled = false
	in := agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "delta", Content: "COMMAND VERSION", Enabled: true}},
		Skills:   []agent.SkillExport{disabledSkill},
	}
	s := NewSurfaces(in, fs)

	_, err := s.Commands.Deliver(dir)
	require.NoError(t, err)
	_, err = s.Skills.Deliver(dir)
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, filepath.Join(dir, ".agents", "skills", "delta", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "COMMAND VERSION", "a skill disabled for agy must not shadow the same-named command")
}
