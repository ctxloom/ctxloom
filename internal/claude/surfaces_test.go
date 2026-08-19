package claude

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// fakePlacement (contextdelivery_test.go) and mcpServersOf (surfacedelivery_test.go)
// are reused here — same package.

// captureStderr redirects os.Stderr around fn and returns what was written. The
// Unsafe adapter's WARN streams through clidiag → os.Stderr with no recorded
// finding, so this is how the commands-Unsafe test observes the loud line.
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
		Context: "# Rules\nthe secret color is vermilion",
		BundleMCP: map[string]wire.MCPServer{
			agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}},
			"config-server":     {Command: "config-cmd", Args: []string{"--flag"}},
			"bundle-server":     {Command: "bundle-cmd", SCM: "ctxloom-bundle:test"},
		},
		Hooks: &wire.HooksConfig{
			Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
			},
		},
		ManageStatusline: true,
		Commands: []agent.CommandExport{
			{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
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

// The seam's contracts, proven by these assignments compiling. Context/MCP/
// settings ADDITIONALLY offer DeliverIsolated (the method SharedRealization
// returns as a value — see TestSurfaces_SharedRealization below); commands is
// Delivery-only, so a SHARED-cwd delivery of it always falls back to the loud
// well-known write (proven live in TestCommandsSurface_Unsafe_WarnsAndProceeds).
var (
	_ agent.Delivery = (*contextSurface)(nil)
	_ agent.Delivery = (*mcpSurface)(nil)
	_ agent.Delivery = (*settingsSurface)(nil)
	_ agent.Delivery = (*commandsSurface)(nil)
	_ interface {
		DeliverIsolated() (agent.Delivered, error)
	} = (*contextSurface)(nil)
	_ interface {
		DeliverIsolated() (agent.Delivered, error)
	} = (*mcpSurface)(nil)
	_ interface {
		DeliverIsolated() (agent.Delivered, error)
	} = (*settingsSurface)(nil)
)

// ---- context surface -------------------------------------------------------

// context Delivery writes CLAUDE.md (the ContextWriter core) into the target
// dir, in the ctxloom-managed section, and its Cleanup removes the file when
// nothing user-authored remains outside the markers.
func TestContextSurface_DeliverWritesCLAUDEmd(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), sampleInputs().Context, "CLAUDE.md holds the assembled context in the managed section")

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"), "cleanup strips the managed section; wholly-managed file is removed")
}

// Hand-authored content in CLAUDE.md outside the managed markers survives both
// Deliver and Cleanup byte-for-byte (a regression pin, at the surface layer
// materialize/run actually drive).
func TestContextSurface_DeliverPreservesHandWrittenCLAUDEmd(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# Team conventions\nalways use tabs\n"), 0644))
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.Context.Deliver(dir)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "always use tabs", "hand-written content survives Deliver")
	assert.Contains(t, string(got), sampleInputs().Context)

	require.NoError(t, handle.Cleanup())
	got, err = os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "always use tabs", "hand-written content survives Cleanup too")
	assert.NotContains(t, string(got), sampleInputs().Context, "the managed section is gone after cleanup")
}

// context DeliverIsolated writes the framed <hash>.sysprompt.md into the
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

// MCP DeliverIsolated writes .mcp.json into the OUT-OF-CWD placement and exposes
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

// settings DeliverIsolated writes .claude/settings.json into the OUT-OF-CWD
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

// ---- commands surface -------------------------------------------------------

// commands Delivery writes .claude/commands/ into the target dir; Cleanup reverts
// the manifest-tracked set.
func TestCommandsSurface_DeliverWritesCommands(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.Commands.Deliver(dir)
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"))

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"), "cleanup reverts the command export")
}

// commands has no out-of-cwd flag and no SharedRealization, so DeliverShared falls
// back to the well-known write — WARNING to stderr AND PROCEEDING (a sanctioned,
// permitted action, never a fatal abort).
func TestCommandsSurface_Unsafe_WarnsAndProceeds(t *testing.T) {
	cwd := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

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
	// It PROCEEDED: the well-known write landed under the shared cwd.
	assert.FileExists(t, filepath.Join(cwd, ".claude", "commands", "review.md"),
		"the well-known write proceeded into the shared cwd")
	require.NoError(t, delivered[0].Cleanup())
}

// ---- skills surface ----------------------------------------------------------

// skills Delivery writes .claude/skills/<name>/SKILL.md (+ sibling files) into
// the target dir with the exec bit preserved; a disabled skill is not written;
// Cleanup reverts the manifest-tracked set. This exercises the same NewSurfaces
// construction the LIVE launch path drives (claudecode.go's buildSurfaces
// forwards SurfaceInputs.Skills straight into this Surfaces value).
func TestSkillsSurface_DeliverWritesSkills(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	handle, err := s.Skills.Deliver(dir)
	require.NoError(t, err)

	skillMD := filepath.Join(dir, ".claude", "skills", "humanize", "SKILL.md")
	require.FileExists(t, skillMD)
	content, err := os.ReadFile(skillMD)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Body.")

	scriptPath := filepath.Join(dir, ".claude", "skills", "humanize", "scripts", "run.sh")
	info, err := os.Stat(scriptPath)
	require.NoError(t, err, "scripts/run.sh must be materialized")
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit on scripts/run.sh survives claude's skills surface")

	assert.NoFileExists(t, filepath.Join(dir, ".claude", "skills", "disabled-skill", "SKILL.md"),
		"a skill with Enabled == false must not be written")

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, skillMD, "cleanup reverts the skill export")
	assert.NoDirExists(t, filepath.Join(dir, ".claude", "skills", "humanize"), "cleanup prunes the now-empty skill directory")
}

// skills has no out-of-cwd flag and no SharedRealization (mirrors commands),
// so DeliverShared falls back to the well-known write — WARNING to stderr AND
// PROCEEDING.
func TestSkillsSurface_Unsafe_WarnsAndProceeds(t *testing.T) {
	cwd := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	r, err := agent.Select(s).WithSkills(agent.SkillsWriteUnsafeFile).Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 1)

	assert.Contains(t, stderr, "warning:", "the fallback streams a loud WARN")
	assert.Contains(t, stderr, "skills")
	assert.Contains(t, stderr, "shared cwd")
	assert.FileExists(t, filepath.Join(cwd, ".claude", "skills", "humanize", "SKILL.md"),
		"the well-known write proceeded into the shared cwd")
	require.NoError(t, delivered[0].Cleanup())
}

// ---- cells wiring (the vertical slice) -------------------------------------

// DeliverShared over a PLAIN WithEverything() selection (no default-derivation —
// that lever lives only at the launch site, launch_backend.go's deliverSet, not
// in the builder itself) resolves context to its TABLE default, UnsafeFile.
// SharedRealization is pair-keyed: only (context, SystemPrompt)
// realizes, so a raw WithEverything selection's UnsafeFile context does NOT
// convert — it falls back to the loud well-known write exactly like
// commands/skills (neither of which has ANY realization). MCP and settings
// still convert via SharedRealization with no warning (their sole approach IS
// the one that realizes) — the builder's shared-cwd terminal packages exactly
// that set for iteration.
func TestSharedCell_AcceptsClaudeRaceSafeSurfaces(t *testing.T) {
	cwd := t.TempDir()
	isolated := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: isolated}, nil)

	r, err := agent.Select(s).WithEverything().Build()
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(cwd)
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 5, "context, MCP, settings, commands, and skills all deliver")
	assert.Equal(t, 3, strings.Count(stderr, "warning:"),
		"context (table-default UnsafeFile, no realization for this pair), commands, and skills all warn; MCP and settings convert silently")
	assert.FileExists(t, filepath.Join(cwd, "CLAUDE.md"),
		"context's well-known write landed in the shared cwd — no realization fired for (context, unsafe-file)")
	assert.NoFileExists(t, filepath.Join(cwd, ".mcp.json"),
		"MCP converted via SharedRealization into the isolated dir, not the shared cwd")
	assert.NoFileExists(t, filepath.Join(cwd, ".claude", "settings.json"),
		"settings converted via SharedRealization into the isolated dir, not the shared cwd")
}

// An isolated cell accepts EVERY surface as a plain Delivery — Deliveries() is the
// iteration set for a worktree / container / materialize target.
func TestDirectoryIsolatedCell_AcceptsAllClaudeSurfaces(t *testing.T) {
	dir := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	// The surfaces reach a cell through the approach-resolved selection — the
	// same path the launch path drives. There is deliberately no raw,
	// unresolved SurfaceSet.Deliveries() to call instead: it materializes the
	// identical tree and has no production caller.
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

	// The private-dir writes all landed at claude's well-known locations.
	assert.FileExists(t, filepath.Join(dir, "CLAUDE.md"))
	assert.FileExists(t, filepath.Join(dir, ".mcp.json"))
	assert.FileExists(t, filepath.Join(dir, ".claude", "settings.json"))
	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"))
}

// ---- approach dispatch (v2) -------------------------------------

// SupportedApproaches pins claude's per-surface table: context offers all three
// approaches (native file, out-of-cwd system-prompt scratch, settings-carried
// hook); mcp/settings/commands offer only the native file.
func TestSurfaces_SupportedApproaches(t *testing.T) {
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	assert.ElementsMatch(t, []agent.Approach{agent.ApproachUnsafeFile, agent.ApproachSystemPrompt, agent.ApproachHook},
		s.SupportedApproaches(agent.SurfaceContext))
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceMCP))
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceSettings))
	assert.Equal(t, []agent.Approach{agent.ApproachUnsafeFile}, s.SupportedApproaches(agent.SurfaceCommands))
}

// DefaultApproach is UnsafeFile (the native file) for every surface claude has —
// never SystemPrompt or Hook, which are explicit caller choices.
func TestSurfaces_DefaultApproach(t *testing.T) {
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands} {
		a, ok := s.DefaultApproach(kind)
		require.True(t, ok, "%s has a default approach", kind)
		assert.Equal(t, agent.ApproachUnsafeFile, a)
	}
}

// SurfaceFor(context, Hook) is a documented no-op: claude's apply-context rides
// the settings-carried inject hook + regenerated cache file, so nothing extra is
// written (a static CLAUDE.md alongside the hook would double the context).
func TestSurfaceFor_ContextHookIsNoOp(t *testing.T) {
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	d, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachHook)
	require.NoError(t, err)
	require.NotNil(t, d, "a real (no-op) Delivery, not nil")

	dir := t.TempDir()
	handle, err := d.Deliver(dir)
	require.NoError(t, err)
	assert.Nil(t, handle, "Hook writes nothing — nil handle, the shared no-op convention")
	assert.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"), "no native file when context rides the hook")
}

// SurfaceFor(context, UnsafeFile | SystemPrompt) both resolve to the SAME
// dual-capable contextSurface instance — the cell (not the surface) decides which
// method to call.
func TestSurfaceFor_ContextUnsafeFileAndSystemPromptShareInstance(t *testing.T) {
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	unsafeFile, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachUnsafeFile)
	require.NoError(t, err)
	systemPrompt, err := s.SurfaceFor(agent.SurfaceContext, agent.ApproachSystemPrompt)
	require.NoError(t, err)
	assert.Same(t, s.Context, unsafeFile)
	assert.Same(t, s.Context, systemPrompt)
}

// An unsupported (kind, approach) combination errors loudly.
func TestSurfaceFor_UnsupportedApproachErrors(t *testing.T) {
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)
	_, err := s.SurfaceFor(agent.SurfaceMCP, agent.ApproachSystemPrompt)
	assert.Error(t, err, "claude's MCP surface has no system-prompt approach")
}

// SharedRealization is PAIR-keyed, not kind-alone: context realizes
// ONLY at ApproachSystemPrompt (ApproachUnsafeFile is the caller's explicit
// native-file request, honored by the DeliverShared fallback instead — see
// TestSharedCell_AcceptsClaudeRaceSafeSurfaces — and ApproachHook is the
// documented no-op, which never even reaches this method via
// deliverOneShared's isolatedDelivery guard). MCP and settings each have
// exactly one approach (ApproachUnsafeFile) and it is the one that realizes,
// so the pair-key changes nothing for them — their --mcp-config/--settings
// launch flags keep firing. Commands/skills have no realization at any
// approach — no out-of-cwd flag exists for either. claude is the only backend
// with any realization at all.
func TestSurfaces_SharedRealization(t *testing.T) {
	isolated := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: isolated}, nil)

	cases := []struct {
		kind     agent.SurfaceKind
		approach agent.Approach
		want     bool
	}{
		{agent.SurfaceContext, agent.ApproachUnsafeFile, false},
		{agent.SurfaceContext, agent.ApproachSystemPrompt, true},
		{agent.SurfaceContext, agent.ApproachHook, false},
		{agent.SurfaceMCP, agent.ApproachUnsafeFile, true},
		{agent.SurfaceSettings, agent.ApproachUnsafeFile, true},
		{agent.SurfaceCommands, agent.ApproachUnsafeFile, false},
		{agent.SurfaceSkills, agent.ApproachUnsafeFile, false},
	}
	for _, tc := range cases {
		realize, ok := s.SharedRealization(tc.kind, tc.approach)
		assert.Equal(t, tc.want, ok, "(%s, %s)", tc.kind, tc.approach)
		if tc.want {
			require.NotNil(t, realize, "(%s, %s)", tc.kind, tc.approach)
		} else {
			assert.Nil(t, realize, "(%s, %s)", tc.kind, tc.approach)
		}
	}
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

// TestNewSurfaces_ThreadsEverySurfaceScopedInput is the pin a past review
// asked for without asking for it. That review read fileTemplateDelivery's
// three surface-scoped fields (mcpCommandOverride, denyTools,
// selfContainedCommands) as a coupling defect because the constructor cannot
// set them and a missed assignment is compile-clean. The assignments are real
// and each is exactly-once, but "compile-clean if missed" is a TEST gap, not
// a constructor problem — a functional-options constructor is just as
// silently omittable.
//
// So this closes the gap: all three inputs are driven from SurfaceInputs through
// NewSurfaces and asserted on the delivered PAYLOAD. Two were already covered
// elsewhere (the delivery parity gate for the MCP command override, the Setup
// deny-tools tests); selfContainedCommands had no surface-level coverage at all,
// which is precisely the field whose omission would silently drop commands from
// a portable materialize target.
func TestNewSurfaces_ThreadsEverySurfaceScopedInput(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	dup := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, dup)

	in := sampleInputs()
	in.Commands = []agent.CommandExport{dup}
	in.SelfContainedCommands = true
	in.MCPCommandOverride = "/usr/local/bin/ctxloom"
	in.DenyTools = []string{"Task"}

	dir := t.TempDir()
	s := NewSurfaces(in, dirPlacement{dir: dir}, nil)

	_, err := s.Commands.Deliver(dir)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "recover.md"),
		"SelfContainedCommands must reach the commands writer — otherwise a portable target silently loses "+
			"every command that happens to exist in the delivering machine's home")

	_, err = s.MCP.Deliver(dir)
	require.NoError(t, err)
	mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)
	assert.Contains(t, string(mcpData), "/usr/local/bin/ctxloom",
		"MCPCommandOverride must reach the ctxloom-managed server's command")

	_, err = s.Settings.Deliver(dir)
	require.NoError(t, err)
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(settingsData), "Task", "DenyTools must reach permissions.deny")
}

// A SHARED-cwd delivery of context resolved at ApproachHook must write NOTHING:
// the context rides the settings-carried SessionStart inject hook, so an
// out-of-cwd scratch write here would hand claude --append-system-prompt-file
// alongside the hook and DOUBLE the delivered context — the exact outcome
// noopContextDelivery exists to prevent. The shared-cwd conversion is keyed on
// SurfaceKind alone, so it must not be applied to a surface the selection
// resolved to the no-op.
func TestDeliverShared_ContextHook_DoesNotWriteSyspromptScratch(t *testing.T) {
	isolated := t.TempDir()
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: isolated}, nil)

	// Settings must ride along: the hook approach is carried by the settings
	// surface, and Build() enforces that pairing.
	resolved, err := agent.Select(s).
		WithContext(agent.ContextWriteHook).
		WithSettings(agent.SettingsWriteUnsafeFile).
		Build()
	require.NoError(t, err)

	live := t.TempDir()
	_, _, errs := resolved.DeliverShared(live)
	require.Empty(t, errs)

	assert.Empty(t, s.Context.Path(), "no --append-system-prompt-file scratch for a hook-carried context")
	entries, err := os.ReadDir(isolated)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), agent.SCMFramedContextSuffix),
			"hook-carried context must not also land an out-of-cwd sysprompt file (%s)", e.Name())
	}
	assert.NoFileExists(t, filepath.Join(live, "CLAUDE.md"), "and no native file either")
}

// armedFailFs fails every write once armed, so a test can let one delivery
// succeed and then force the NEXT one to fail on the same surface instance.
type armedFailFs struct {
	afero.Fs
	armed *bool
}

var errArmedWrite = errors.New("armed write failure")

func (f armedFailFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if *f.armed && flag&os.O_CREATE != 0 {
		return nil, errArmedWrite
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func (f armedFailFs) Create(name string) (afero.File, error) {
	if *f.armed {
		return nil, errArmedWrite
	}
	return f.Fs.Create(name)
}

func (f armedFailFs) Rename(oldname, newname string) error {
	if *f.armed {
		return errArmedWrite
	}
	return f.Fs.Rename(oldname, newname)
}

func (f armedFailFs) MkdirAll(path string, perm os.FileMode) error {
	if *f.armed {
		return errArmedWrite
	}
	return f.Fs.MkdirAll(path, perm)
}

// Path() documents itself as "" before delivery, and buildArgs relies on that:
// flagArgs must never hand claude --mcp-config / --settings naming a file that
// was not written. A FAILED DeliverIsolated therefore has to leave Path()
// reporting nothing, not the path recorded by an earlier successful call.
func TestSurfaces_FailedDeliverIsolated_ClearsPath(t *testing.T) {
	var armed bool
	fs := armedFailFs{Fs: afero.NewMemMapFs(), armed: &armed}
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: "/iso"}, fs)

	for _, tc := range []struct {
		name    string
		deliver func() (agent.Delivered, error)
		path    func() string
	}{
		{"mcp", s.MCP.DeliverIsolated, s.MCP.Path},
		{"settings", s.Settings.DeliverIsolated, s.Settings.Path},
		{"context", s.Context.DeliverIsolated, s.Context.Path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			armed = false
			_, err := tc.deliver()
			require.NoError(t, err)
			require.NotEmpty(t, tc.path(), "the successful delivery records its path")

			armed = true
			_, err = tc.deliver()
			require.Error(t, err, "the armed fs must fail the second delivery")
			assert.Empty(t, tc.path(), "a failed delivery must not leave a path naming a file that was not written")
		})
	}
	// And flagArgs, the buildArgs consumer, emits no flag for any of them.
	assert.Empty(t, s.flagArgs(), "no launch flag may name a file that was not written")
}

// claude enumerates its five surfaces in three places that must agree: the
// typed struct fields (which flagArgs and SharedRealization read by name), the
// kind-keyed dispatch map SurfaceFor resolves against, and the declared
// claudeApproaches table Build() validates against. None can be derived from
// the others without losing the typed accessors, so the agreement is pinned
// instead: a surface added to one and missed in another fails here rather than
// silently resolving to nothing at launch.
func TestSurfaces_TableDispatchAndFieldsEnumerateTheSameSurfaces(t *testing.T) {
	s := NewSurfaces(sampleInputs(), fakePlacement{dir: t.TempDir()}, nil)

	byKind := map[agent.SurfaceKind]agent.Delivery{
		agent.SurfaceContext:  s.Context,
		agent.SurfaceMCP:      s.MCP,
		agent.SurfaceSettings: s.Settings,
		agent.SurfaceCommands: s.Commands,
		agent.SurfaceSkills:   s.Skills,
	}
	assert.Len(t, byKind, len(claudeApproaches),
		"every kind in the declared approach table needs a typed field here (and vice versa)")

	for kind, field := range byKind {
		approaches, ok := claudeApproaches[kind]
		require.True(t, ok, "%s is a struct field with no entry in the approach table", kind)
		require.NotEmpty(t, approaches)

		d, err := s.SurfaceFor(kind, agent.ApproachUnsafeFile)
		require.NoError(t, err, "%s must resolve at its native approach", kind)
		assert.Same(t, field, d, "%s must resolve to the SAME instance the struct field holds", kind)
	}
}
