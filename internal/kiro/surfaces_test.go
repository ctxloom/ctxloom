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
	}
}

// sampleSkill builds a minimal enabled SkillExport with SKILL.md content
// distinguishable from a command's rendered content, for the D6 clash tests.
func sampleSkill(name, body string) agent.SkillExport {
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

// ---- compile-time capability guarantees -----------------------------------
//
// Every kiro surface is Delivery-ONLY at this layer (the --agent name lever is an
// orthogonal launch concern) and has no SharedRealization, proven by these
// assignments compiling.
var (
	_ agent.Delivery = (*contextSurface)(nil)
	_ agent.Delivery = (*mcpSurface)(nil)
	_ agent.Delivery = (*settingsSurface)(nil)
	_ agent.Delivery = (*agent.ManagedCommandsDelivery)(nil) // kiro's commands surface
)

// No kiro surface has a SharedRealization, so a SHARED-cwd delivery of any kiro
// surface always falls back to the loud well-known write — proven in
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

// ---- commands surface (.kiro/skills/) --------------------------------------

func TestCommandsSurface_DeliverWritesSkillMd(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	s := NewSurfaces(sampleInputs(), fs)

	handle, err := s.Commands.Deliver(dir)
	require.NoError(t, err)

	skillPath := filepath.Join(dir, kiroDir, "skills", "review", "SKILL.md")
	exists, _ := afero.Exists(fs, skillPath)
	assert.True(t, exists, "the SKILL.md lands under .kiro/skills/<name>/")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, skillPath)
	assert.False(t, exists, "cleanup reverts the manifest-tracked command")
}

// ---- skills surface (.kiro/skills/, D6 collision with commands) -----------

// TestSkillsSurface_DeliverWritesSkillPackage proves the skills surface lands
// a full package (SKILL.md + sibling files) under .kiro/skills/<name>/ and
// that Cleanup reverts exactly that set.
func TestSkillsSurface_DeliverWritesSkillPackage(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	in := sampleInputs()
	in.Skills = []agent.SkillExport{sampleSkill("humanize", "skill body")}
	s := NewSurfaces(in, fs)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, filepath.Join(dir, kiroDir, "skills", "humanize", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "skill body", string(data))
	exists, _ := afero.Exists(fs, filepath.Join(dir, kiroDir, "skills", "humanize", "assets", "note.txt"))
	assert.True(t, exists, "sibling files travel with the skill package")

	require.NoError(t, handle.Cleanup())
	exists, _ = afero.Exists(fs, filepath.Join(dir, kiroDir, "skills", "humanize", "SKILL.md"))
	assert.False(t, exists, "cleanup reverts the manifest-tracked skill")
}

// TestCoexistence_DifferentNamedCommandAndSkillBothSurvive proves a command
// "alpha" and a skill "beta" (DIFFERENT names) both materialize under
// .kiro/skills/ and each survives the OTHER surface's cleanup — the
// non-collision half of D6, proving the two-manifest split doesn't step on
// unrelated files.
func TestCoexistence_DifferentNamedCommandAndSkillBothSurvive(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	in := agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "alpha", Content: "command alpha body", Enabled: true}},
		Skills:   []agent.SkillExport{sampleSkill("beta", "skill beta body")},
	}
	s := NewSurfaces(in, fs)

	cmdHandle, err := s.Commands.Deliver(dir)
	require.NoError(t, err)
	skillHandle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	cmdData, err := afero.ReadFile(fs, filepath.Join(dir, kiroDir, "skills", "alpha", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(cmdData), "command alpha body")

	skillData, err := afero.ReadFile(fs, filepath.Join(dir, kiroDir, "skills", "beta", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "skill beta body", string(skillData))

	// Cleaning up the SKILLS surface must not touch the command's file.
	require.NoError(t, skillHandle.Cleanup())
	exists, _ := afero.Exists(fs, filepath.Join(dir, kiroDir, "skills", "alpha", "SKILL.md"))
	assert.True(t, exists, "cleaning up skills leaves the unrelated command file alone")
	exists, _ = afero.Exists(fs, filepath.Join(dir, kiroDir, "skills", "beta", "SKILL.md"))
	assert.False(t, exists, "the skill itself is gone")

	// Re-deliver the skill, then clean up the COMMANDS surface — must not
	// touch the skill's file.
	skillHandle, err = s.Skills.Deliver(dir)
	require.NoError(t, err)
	require.NoError(t, cmdHandle.Cleanup())
	exists, _ = afero.Exists(fs, filepath.Join(dir, kiroDir, "skills", "alpha", "SKILL.md"))
	assert.False(t, exists, "the command itself is gone")
	skillData, err = afero.ReadFile(fs, filepath.Join(dir, kiroDir, "skills", "beta", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "skill beta body", string(skillData), "cleaning up commands leaves the unrelated skill file alone")
	require.NoError(t, skillHandle.Cleanup())
}

// TestClash_SkillWinsOverSameNamedCommand is the D6 crux test: a command
// "gamma" and a skill "gamma" collide on the identical path
// .kiro/skills/gamma/SKILL.md. Materializing kiro's full surface set must
// leave the SKILL's content (and its sibling files) at that path, never the
// command's, and the command's manifest (kiroManifest) must not claim the
// path the skill wrote — no double-ownership, proven by cleaning up the
// COMMANDS surface alone and observing the skill's file survives untouched.
func TestClash_SkillWinsOverSameNamedCommand(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	in := agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "gamma", Content: "COMMAND VERSION", Enabled: true}},
		Skills:   []agent.SkillExport{sampleSkill("gamma", "SKILL VERSION")},
	}
	s := NewSurfaces(in, fs)

	// Deliver in the same order Deliveries() uses: commands then skills.
	cmdHandle, err := s.Commands.Deliver(dir)
	require.NoError(t, err)
	skillHandle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	path := filepath.Join(dir, kiroDir, "skills", "gamma", "SKILL.md")
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, "SKILL VERSION", string(data), "the skill package wins; the command's content must not be present")

	exists, _ := afero.Exists(fs, filepath.Join(dir, kiroDir, "skills", "gamma", "assets", "note.txt"))
	assert.True(t, exists, "the skill's sibling files are present too")

	// The commands writer never saw "gamma" (agent.FilterCommandsClaimedBySkills dropped
	// it before WriteCommandFiles ran), so its manifest must not list the
	// path the skill owns — proven by cleaning up ONLY the commands surface
	// and observing the skill's file is untouched.
	require.NoError(t, cmdHandle.Cleanup())
	data, err = afero.ReadFile(fs, path)
	require.NoError(t, err, "the commands cleanup must not have deleted the skill's file")
	assert.Equal(t, "SKILL VERSION", string(data), "no double-ownership: commands cleanup left the skill's winning file intact")

	// Now clean up the skills surface: the path is finally removed.
	require.NoError(t, skillHandle.Cleanup())
	exists, _ = afero.Exists(fs, path)
	assert.False(t, exists, "skills cleanup removes the file it owns")
}

// TestClash_SkillWinsWarnsOperator proves the OTHER half of the D6 resolution:
// besides winning the file, a genuine command+enabled-skill collision must
// LOUDLY tell the operator the command was dropped (agent.Warn → stderr). This
// guards the warning path itself — previously only the file-wins outcome was
// unit-tested, so a regression that silently stopped emitting the warning (e.g.
// a builtin command disappearing, or the claim check skewing) could slip
// through green. Driven through the real kiro NewSurfaces routing so the whole
// chain (NewSkillShapedCommandsAndSkills → FilterCommandsClaimedBySkills →
// Warn) is exercised, not just the shared helper in isolation.
func TestClash_SkillWinsWarnsOperator(t *testing.T) {
	fs := afero.NewMemMapFs()
	in := agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "gamma", Content: "COMMAND VERSION", Enabled: true}},
		Skills:   []agent.SkillExport{sampleSkill("gamma", "SKILL VERSION")},
	}

	// NewSurfaces filters commands through FilterCommandsClaimedBySkills, which
	// emits the warn when it drops a claimed command; capture stderr around it.
	out := captureStderr(t, func() { NewSurfaces(in, fs) })

	assert.Contains(t, out, `skill "gamma" wins over command of the same name`,
		"a genuine collision must warn the operator the command was dropped")
	assert.Contains(t, out, "kiro", "the warning must name the engine")
	assert.Contains(t, out, "SKILL.md", "the warning must name the path the command was not written to")
}

// TestClash_NoCollisionDoesNotWarn is the negative guard: a skill whose name
// matches NO command claims nothing, so no operator warning is emitted (the
// skill still writes its own file — proven elsewhere). This pins the exact
// regression that broke skill.feature's kiro scenario when the /recover builtin
// command was removed: with no command of the skill's name in the export set,
// there is nothing to warn about.
func TestClash_NoCollisionDoesNotWarn(t *testing.T) {
	fs := afero.NewMemMapFs()
	in := agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "review", Content: "Review the diff", Enabled: true}},
		Skills:   []agent.SkillExport{sampleSkill("gamma", "SKILL VERSION")},
	}

	out := captureStderr(t, func() { NewSurfaces(in, fs) })

	assert.NotContains(t, out, "wins over command of the same name",
		"no name collision means no skill-wins warning")
}

// TestClash_DisabledSkillDoesNotShadowCommand proves the collision rule is
// scoped to skills ENABLED for kiro: a skill named "delta" disabled for this
// engine must not claim the name, so the command "delta" still writes its
// SKILL.md.
func TestClash_DisabledSkillDoesNotShadowCommand(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	disabledSkill := sampleSkill("delta", "SKILL VERSION")
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

	data, err := afero.ReadFile(fs, filepath.Join(dir, kiroDir, "skills", "delta", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "COMMAND VERSION", "a skill disabled for kiro must not shadow the same-named command")
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
	exists, _ := afero.Exists(fs, filepath.Join(cwd, kiroDir, "skills", "review", "SKILL.md"))
	assert.True(t, exists, "the well-known write proceeded into the shared cwd")
	require.NoError(t, delivered[0].Cleanup())
}

// ---- cells wiring ----------------------------------------------------------

func TestDirectoryIsolatedCell_AcceptsAllKiroSurfaces(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/worktree"
	in := sampleInputs()
	in.Skills = []agent.SkillExport{sampleSkill("humanize", "skill body")}
	s := NewSurfaces(in, fs)

	// The surfaces reach a cell through the approach-resolved selection — the
	// same path the launch path drives. (U033-F05 deleted the raw, unresolved
	// SurfaceSet.Deliveries() this used to call; it materialized the identical
	// tree and had no production caller.)
	resolved, err := agent.Select(s).WithEverything().Build()
	require.NoError(t, err)
	ds := resolved.Deliveries()
	require.Len(t, ds, 5, "context, MCP, settings, commands, skills")

	cell := agent.NewIsolatedCell(dir)
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
		filepath.Join(kiroDir, "skills", "humanize", "SKILL.md"),
	} {
		exists, _ := afero.Exists(fs, filepath.Join(dir, rel))
		assert.True(t, exists, "expected %s", rel)
	}
}

func TestSharedCell_AcceptsOnlyUnsafeKiroSurfaces(t *testing.T) {
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
	assert.Contains(t, stderr, "warning:", "every kiro surface warns when DeliverShared delivers it")
}

// ---- approach dispatch (vital-tiger v2) -------------------------------------

// SupportedApproaches pins kiro's per-surface table: every surface is
// native-file-only — kiro reads steering directly, no hook, no out-of-cwd flag.
func TestSurfaces_SupportedApproaches(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(kind), "%s", kind)
	}
}

// DefaultApproach is the native file for every kind kiro has.
func TestSurfaces_DefaultApproach(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		a, ok := s.DefaultApproach(kind)
		require.True(t, ok, "%s", kind)
		assert.Equal(t, agent.ApproachUnsafeFile, a)
	}
}

// A non-UnsafeFile approach (Hook) is unsupported for kiro's context — kiro reads
// steering directly, never a SessionStart hook.
func TestSurfaceFor_ContextHookUnsupported(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	_, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachHook)
	assert.Error(t, err, "kiro context is FILE-only")
}

// SharedRealization is absent for every kind: kiro has no out-of-cwd redirect, so
// a SHARED-cwd delivery always falls back to the loud well-known write.
func TestSurfaces_SharedRealization_Absent(t *testing.T) {
	s := NewSurfaces(sampleInputs(), afero.NewMemMapFs())
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		_, ok := s.SharedRealization(kind)
		assert.False(t, ok, "%s: kiro has no out-of-cwd realization", kind)
	}
}
