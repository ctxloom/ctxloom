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
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
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

// runRoots advises a run rooted at project with its out-of-cwd scratch at
// scratch, on the host — the two roots claude's well-known and isolated
// realizations read respectively.
func runRoots(project, scratch string) present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project},
		Scratch:     present.Root{Host: scratch},
	}))
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

// ---- constructing claude's approaches for a test -----------------------------

// builtSurfaces holds one constructed instance of each claude approach, so a
// test can drive a surface directly (the field) or through the builder
// (Surfaces, the Declaration). Native is the unsafe-file context approach;
// Context is the system-prompt one, which writes the SAME CLAUDE.md through
// Deliver and the out-of-cwd scratch through DeliverIsolated.
type builtSurfaces struct {
	Native   agent.Approach
	Context  *systemPromptContext
	Hook     agent.Approach
	MCP      *mcpSurface
	Settings *settingsSurface
	Commands *commandsSurface
	Skills   agent.Approach
}

// newSurfaces constructs every claude approach from in through the
// Declaration — the same path Build takes — and type-asserts the concrete
// ones, so a test that reaches a field is reaching what a launch would.
func newSurfaces(in agent.SurfaceInputs, fs afero.Fs) builtSurfaces {
	must := func(kind agent.SurfaceKind, name string) agent.Approach {
		a, ok := Surfaces.Construct(kind, name, in, fs)
		if !ok {
			panic("claude does not declare " + kind.String() + "=" + name)
		}
		return a
	}
	return builtSurfaces{
		Native:   must(agent.SurfaceContext, agent.ApproachUnsafeFile),
		Context:  must(agent.SurfaceContext, ApproachSystemPrompt).(*systemPromptContext),
		Hook:     must(agent.SurfaceContext, agent.ApproachHook),
		MCP:      must(agent.SurfaceMCP, agent.ApproachUnsafeFile).(*mcpSurface),
		Settings: must(agent.SurfaceSettings, agent.ApproachUnsafeFile).(*settingsSurface),
		Commands: must(agent.SurfaceCommands, agent.ApproachUnsafeFile).(*commandsSurface),
		Skills:   must(agent.SurfaceSkills, agent.ApproachUnsafeFile),
	}
}

// ---- context surface -------------------------------------------------------

// The NATIVE-FILE context approach writes CLAUDE.md (the ContextWriter core)
// into the target dir, in the ctxloom-managed section, and the file SURVIVES
// Cleanup: a
// project surface outlives the run that delivered it
// (agent.SurfacePersistsAfterExit), so the handle reverses nothing. Teardown
// belongs to per-session scratch, which this is not.
func TestContextSurface_DeliverWritesCLAUDEmd(t *testing.T) {
	dir := t.TempDir()
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Native.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), sampleInputs().Context, "CLAUDE.md holds the assembled context in the managed section")

	require.NoError(t, handle.Cleanup())
	after, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err, "a project surface survives the run that delivered it")
	assert.Contains(t, string(after), sampleInputs().Context, "Cleanup reverses nothing for a project surface")
}

// Hand-authored content in CLAUDE.md outside the managed markers survives
// Deliver byte-for-byte (a regression pin, at the surface layer
// materialize/run actually drive), and Cleanup leaves the whole file — both
// halves — in place, per agent.SurfacePersistsAfterExit.
func TestContextSurface_DeliverPreservesHandWrittenCLAUDEmd(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# Team conventions\nalways use tabs\n"), 0644))
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Native.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "always use tabs", "hand-written content survives Deliver")
	assert.Contains(t, string(got), sampleInputs().Context)

	require.NoError(t, handle.Cleanup())
	got, err = os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "always use tabs", "hand-written content survives Cleanup too")
	assert.Contains(t, string(got), sampleInputs().Context, "the managed section survives Cleanup: a project surface persists")
}

// The system-prompt approach's ONE form writes the framed <hash>.sysprompt.md
// beneath the private root (via appendFlagDelivery) and exposes it via Path()
// — and does NOT touch the well-known CLAUDE.md. Every cell reaches this form;
// there is no second one for a cell to pick instead.
func TestContextSurface_DeliverWritesSyspromptAndExposesPath(t *testing.T) {
	isolated := t.TempDir()
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Context.Deliver(runRoots(t.TempDir(), isolated))
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
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.MCP.Deliver(present.ProjectOnHost(dir))
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
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.MCP.DeliverIsolated(runRoots(cwd, isolated))
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
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Settings.Deliver(present.ProjectOnHost(dir))
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
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Settings.DeliverIsolated(runRoots(cwd, isolated))
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
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Commands.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"))

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(dir, ".claude", "commands", "review.md"), "cleanup reverts the command export")
}

// commands has no out-of-cwd flag and no out-of-cwd form, so DeliverShared falls
// back to the well-known write — WARNING to stderr AND PROCEEDING (a sanctioned,
// permitted action, never a fatal abort).
func TestCommandsSurface_Unsafe_WarnsAndProceeds(t *testing.T) {
	cwd := t.TempDir()

	r, err := agent.Select(Surfaces).With(agent.SurfaceCommands, agent.ApproachUnsafeFile).Build(sampleInputs(), nil)
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(present.ProjectOnHost(cwd))
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
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Skills.Deliver(present.ProjectOnHost(dir))
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
	assert.FileExists(t, skillMD, "a delivered skill package persists after the run (SurfacePersistsAfterExit)")
}

// skills has no out-of-cwd flag and no out-of-cwd form (mirrors commands),
// so DeliverShared falls back to the well-known write — WARNING to stderr AND
// PROCEEDING.
func TestSkillsSurface_Unsafe_WarnsAndProceeds(t *testing.T) {
	cwd := t.TempDir()
	r, err := agent.Select(Surfaces).With(agent.SurfaceSkills, agent.ApproachUnsafeFile).Build(sampleInputs(), nil)
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(present.ProjectOnHost(cwd))
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
// out-of-cwd form is pair-keyed: only (context, SystemPrompt)
// realizes, so a raw WithEverything selection's UnsafeFile context does NOT
// convert — it falls back to the loud well-known write exactly like
// commands/skills (neither of which has ANY realization). MCP and settings
// still convert via out-of-cwd form with no warning (their sole approach IS
// the one that realizes) — the builder's shared-cwd terminal packages exactly
// that set for iteration.
func TestSharedCell_AcceptsClaudeRaceSafeSurfaces(t *testing.T) {
	cwd := t.TempDir()
	isolated := t.TempDir()
	r, err := agent.Select(Surfaces).WithEverything().Build(sampleInputs(), nil)
	require.NoError(t, err)

	var delivered []agent.Delivered
	stderr := captureStderr(t, func() {
		var errs []error
		delivered, _, errs = r.DeliverShared(runRoots(cwd, isolated))
		require.Empty(t, errs)
	})
	require.Len(t, delivered, 5, "context, MCP, settings, commands, and skills all deliver")
	assert.Equal(t, 3, strings.Count(stderr, "warning:"),
		"context (table-default UnsafeFile, no realization for this pair), commands, and skills all warn; MCP and settings convert silently")
	assert.FileExists(t, filepath.Join(cwd, "CLAUDE.md"),
		"context's well-known write landed in the shared cwd — no realization fired for (context, unsafe-file)")
	assert.NoFileExists(t, filepath.Join(cwd, ".mcp.json"),
		"MCP converted via out-of-cwd form into the isolated dir, not the shared cwd")
	assert.NoFileExists(t, filepath.Join(cwd, ".claude", "settings.json"),
		"settings converted via out-of-cwd form into the isolated dir, not the shared cwd")
}

// An isolated cell accepts EVERY surface as a plain Delivery — Deliveries() is the
// iteration set for a worktree / container / materialize target.
func TestDirectoryIsolatedCell_AcceptsAllClaudeSurfaces(t *testing.T) {
	dir := t.TempDir()
	// The surfaces reach a cell through the approach-resolved selection — the
	// same path the launch path drives. There is deliberately no raw,
	// unresolved SurfaceSet.Deliveries() to call instead: it materializes the
	// identical tree and has no production caller.
	resolved, err := agent.Select(Surfaces).WithEverything().Build(sampleInputs(), nil)
	require.NoError(t, err)
	ds := resolved.Deliveries()
	require.Len(t, ds, 5, "context, MCP, settings, commands, skills")

	cell := agent.NewIsolatedCell(present.ProjectOnHost(dir))
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

// ---- the declaration ---------------------------------------------------------

// Surfaces pins claude's per-surface declaration: context offers all three
// approaches (native file, out-of-cwd system prompt, settings-carried hook);
// settings offers the project file and the engine-home record write;
// mcp/commands/skills offer only the native file; and the default
// everywhere is the native file — named, not positional.
func TestSurfaces_DeclaresContextThreeWaysSettingsTwoAndTheRestOnce(t *testing.T) {
	assert.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachSystemPrompt, agent.ApproachHook},
		Surfaces.Names(agent.SurfaceContext))
	assert.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachHewRecord}, Surfaces.Names(agent.SurfaceSettings))
	for _, kind := range []agent.SurfaceKind{agent.SurfaceMCP, agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.Equal(t, []string{agent.ApproachUnsafeFile}, Surfaces.Names(kind), "%s", kind)
	}
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		def, ok := Surfaces.Default(kind)
		require.True(t, ok, "%s has a default approach", kind)
		assert.Equal(t, agent.ApproachUnsafeFile, def)
	}
}

// The hook approach is the shared Rider: it writes nothing (nil handle, no
// CLAUDE.md) and rides the settings surface, which carries the hook.
func TestSurfaces_ContextHookIsANoOpRider(t *testing.T) {
	s := newSurfaces(sampleInputs(), nil)

	rider, ok := s.Hook.(agent.Rider)
	require.True(t, ok, "hook-carried context rides another surface")
	assert.Equal(t, agent.SurfaceSettings, rider.Rides())

	dir := t.TempDir()
	handle, err := s.Hook.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	assert.Nil(t, handle, "Hook writes nothing — nil handle, the shared no-op convention")
	assert.NoFileExists(t, filepath.Join(dir, "CLAUDE.md"), "no native file when context rides the hook")
}

// Whether an approach is safe in a shared cwd is a property of each VALUE, and
// it is asserted on the BEHAVIOUR — where the bytes land — not on the marker
// interface that used to carry it. The distinction is the point of this test:
// the system prompt's safety was previously readable only as "it implements
// OutOfCwd, so a shared launch converts it", and that same marker was what
// silently converted an ISOLATED launch's selection into a CLAUDE.md. The
// property that actually matters survives the marker's removal — the system
// prompt's bytes never land in the workspace, on ANY cell.
//
// The native-file context is deliberately NOT safe: an explicit unsafe-file
// request is honoured, and warned. commands and skills have no private form at
// all. The system prompt alone is LaunchOnly — refused at rest, where nothing
// can sink its flag.
func TestSurfaces_SharedCwdSafetyAndLaunchOnly(t *testing.T) {
	s := newSurfaces(sampleInputs(), nil)
	launchOnly := func(a agent.Approach) bool { _, ok := a.(agent.LaunchOnly); return ok }

	assert.False(t, agent.SafeInSharedCwd(s.Native), "context unsafe-file: honoured natively, never converted")
	assert.True(t, agent.SafeInSharedCwd(s.Context), "the system prompt stays out of the workspace")
	assert.True(t, agent.SafeInSharedCwd(s.Hook), "a rider writes no bytes of its own")
	assert.True(t, agent.SafeInSharedCwd(s.MCP))
	assert.True(t, agent.SafeInSharedCwd(s.Settings))
	assert.False(t, agent.SafeInSharedCwd(s.Commands))
	assert.False(t, agent.SafeInSharedCwd(s.Skills))

	// The system prompt's safety is STRUCTURAL, not a conversion a cell opts
	// into: its presentation is outside the project root, so the property holds
	// on an isolated cell too — which is exactly what the silent conversion
	// used to break.
	assert.False(t, agent.PresentsUnderProjectRoot(s.Context),
		"the framed system prompt must never present as a project file")
	assert.True(t, agent.PresentsUnderProjectRoot(s.Native),
		"CLAUDE.md is a project file — that is what makes unsafe-file unsafe")

	assert.True(t, launchOnly(s.Context), "system-prompt has no argv sink at rest")
	for _, a := range []agent.Approach{s.Native, s.Hook, s.MCP, s.Settings, s.Commands, s.Skills} {
		assert.False(t, launchOnly(a))
	}
}

// An approach claude does not declare for a surface is refused by Build,
// naming what IS declared.
func TestSurfaces_UndeclaredApproachIsRefused(t *testing.T) {
	_, err := agent.Select(Surfaces).With(agent.SurfaceMCP, ApproachSystemPrompt).Build(sampleInputs(), nil)
	require.Error(t, err, "claude's MCP surface has no system-prompt approach")
	assert.Contains(t, err.Error(), agent.ApproachUnsafeFile)
}

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
	s := newSurfaces(in, nil)

	_, err := s.Commands.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "recover.md"),
		"SelfContainedCommands must reach the commands writer — otherwise a portable target silently loses "+
			"every command that happens to exist in the delivering machine's home")

	_, err = s.MCP.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	mcpData, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)
	assert.Contains(t, string(mcpData), "/usr/local/bin/ctxloom",
		"MCPCommandOverride must reach the ctxloom-managed server's command")

	_, err = s.Settings.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(settingsData), "Task", "DenyTools must reach permissions.deny")
}

// A SHARED-cwd delivery of context resolved at ApproachHook must write NOTHING:
// the context rides the settings-carried SessionStart inject hook, so an
// out-of-cwd scratch write here would hand claude --append-system-prompt-file
// alongside the hook and DOUBLE the delivered context — the exact outcome the
// hook approach's no-op Deliver exists to prevent. The out-of-cwd form is a
// property of the approach VALUE, so the one the selection resolved (a
// Rider) has none to run.
func TestDeliverShared_ContextHook_DoesNotWriteSyspromptScratch(t *testing.T) {
	isolated := t.TempDir()

	// Settings must ride along: the hook approach is carried by the settings
	// surface, and Build() enforces that pairing.
	resolved, err := agent.Select(Surfaces).
		With(agent.SurfaceContext, agent.ApproachHook).
		With(agent.SurfaceSettings, agent.ApproachUnsafeFile).
		Build(sampleInputs(), nil)
	require.NoError(t, err)

	live := t.TempDir()
	_, _, errs := resolved.DeliverShared(runRoots(live, isolated))
	require.Empty(t, errs)

	for _, ra := range resolved.Approaches() {
		_, isSysprompt := ra.Approach.(*systemPromptContext)
		assert.False(t, isSysprompt, "no --append-system-prompt-file scratch for a hook-carried context")
	}
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
	// Built through the Declaration so flagArgs below reads the very
	// instances that delivered — the way buildArgs does after Setup.
	resolved, err := agent.Select(Surfaces).WithEverything().With(agent.SurfaceContext, ApproachSystemPrompt).Build(sampleInputs(), fs)
	require.NoError(t, err)
	var s builtSurfaces
	for _, ra := range resolved.Approaches() {
		switch a := ra.Approach.(type) {
		case *systemPromptContext:
			s.Context = a
		case *mcpSurface:
			s.MCP = a
		case *settingsSurface:
			s.Settings = a
		}
	}
	require.NotNil(t, s.Context)
	require.NotNil(t, s.MCP)
	require.NotNil(t, s.Settings)

	roots := runRoots("/proj", "/iso")
	for _, tc := range []struct {
		name    string
		deliver func(present.Start) (agent.Delivered, error)
		path    func() string
	}{
		{"mcp", s.MCP.DeliverIsolated, s.MCP.Path},
		{"settings", s.Settings.DeliverIsolated, s.Settings.Path},
		{"context", s.Context.Deliver, s.Context.Path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			armed = false
			_, err := tc.deliver(roots)
			require.NoError(t, err)
			require.NotEmpty(t, tc.path(), "the successful delivery records its path")

			armed = true
			_, err = tc.deliver(roots)
			require.Error(t, err, "the armed fs must fail the second delivery")
			assert.Empty(t, tc.path(), "a failed delivery must not leave a path naming a file that was not written")
		})
	}
	// And flagArgs, the buildArgs consumer, emits no flag for any of them.
	assert.Empty(t, flagArgs(resolved), "no launch flag may name a file that was not written")
}

// Every approach's Present must name the path that approach actually writes.
// Without this, the presentation of a surface with no out-of-cwd flag
// (commands, skills) is read by nothing: the isolated --mcp-config and
// --settings paths and flagArgs cover the other three, so a wrong rel path in
// the commands or skills presentation would leave the suite green — a
// declaration that documents nothing and gates nothing.
//
// It walks the DECLARATION rather than a list repeated here, so a surface
// added to Surfaces is covered the moment it is declared.
func TestSurfaces_PresentedPathIsWhereTheApproachWrites(t *testing.T) {
	for kind := range Surfaces {
		t.Run(kind.String(), func(t *testing.T) {
			dir := t.TempDir()
			def, ok := Surfaces.Default(kind)
			require.True(t, ok, "%s is declared, so it must have a default", kind)
			a, ok := Surfaces.Construct(kind, def, sampleInputs(), nil)
			require.True(t, ok)

			start := present.ProjectOnHost(dir)
			_, err := a.Deliver(start)
			require.NoError(t, err)

			declared := a.Present(start).HostPath
			_, statErr := os.Stat(declared)
			require.NoError(t, statErr,
				"%s presents %q but delivered nothing there", kind, declared)
		})
	}
}

// The same declared approach, constructed once, lands in DIFFERENT places for
// a host run and a worktree run: roots bind at Present/Deliver, never at
// construction — which is what keeps a worktree-isolated agent out of the
// coordinator's checkout. Enumerating the declaration needs neither root.
func TestSurfaces_RootsBindPerLaunchNotAtConstruction(t *testing.T) {
	a, ok := Surfaces.Construct(agent.SurfaceContext, agent.ApproachUnsafeFile, sampleInputs(), nil)
	require.True(t, ok)
	host := a.Present(present.ProjectOnHost("/home/dev/project")).HostPath
	worktree := a.Present(present.ProjectOnHost("/home/dev/worktrees/project--feat")).HostPath
	assert.NotEqual(t, host, worktree)
	assert.Equal(t, filepath.Join("/home/dev/project", ContextFileName), host)
	assert.Equal(t, filepath.Join("/home/dev/worktrees/project--feat", ContextFileName), worktree)
	assert.NotEmpty(t, Surfaces.Names(agent.SurfaceContext), "enumeration needs no root and no construction")
}

// contextPathOf, mcpPathOf and settingsPathOf read, after Setup, the
// out-of-cwd path each delivered approach recorded — through the SAME
// resolved selection buildArgs reads (LaunchBackend.Resolved). "" when the
// approach was not selected or wrote nothing, matching Path()'s own contract.
func contextPathOf(b *ClaudeCode) string  { return resolvedPath[*systemPromptContext](b) }
func mcpPathOf(b *ClaudeCode) string      { return resolvedPath[*mcpSurface](b) }
func settingsPathOf(b *ClaudeCode) string { return resolvedPath[*settingsSurface](b) }

func resolvedPath[T pathed](b *ClaudeCode) string {
	r := b.Resolved()
	if r == nil {
		return ""
	}
	for _, ra := range r.Approaches() {
		if a, ok := ra.Approach.(T); ok {
			return a.Path()
		}
	}
	return ""
}
