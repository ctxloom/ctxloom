package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// fakePlacement (contextdelivery_test.go) and mcpServersOf (surfacedelivery_test.go)
// are reused here — same package.

// runRoots advises a run rooted at project with its session home at
// sessionHome, on the host — the roots claude's well-known and private-root
// approaches read respectively. Tests advise DISTINCT dirs so an assertion on
// one root cannot be satisfied by a write to another.
func runRoots(project, sessionHome string) present.Start {
	return present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project},
		SessionHome: present.Root{Host: sessionHome},
	}))
}

// sampleInputs is a representative, fully-populated SurfaceInputs.
func sampleInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Reporter: strictness.Sink("ctxloom"),
		Context:  "# Rules\nthe secret color is vermilion",
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
// Context is the system-prompt one (the framed <hash>.sysprompt.md beneath
// the session home).
type builtSurfaces struct {
	Native    agent.Approach
	Context   *systemPromptContext
	Hook      agent.Approach
	MCP       *mcpConfig
	MCPUnsafe *mcpUnsafeFile
	Settings  *settingsSurface
	Commands  *commandsSurface
	Skills    agent.Approach
}

// newSurfaces constructs every claude approach from in through the
// Declaration — the same path Build takes — and type-asserts the concrete
// ones, so a test that reaches a field is reaching what a launch would.
func newSurfaces(in agent.SurfaceInputs, fs afero.Fs) builtSurfaces {
	must := func(kind agent.SurfaceKind, name string) agent.Approach {
		a, ok := testDeclaration().Construct(kind, name, in, fs)
		if !ok {
			panic("claude does not declare " + kind.String() + "=" + name)
		}
		return a
	}
	return builtSurfaces{
		Native:    must(agent.SurfaceContext, agent.ApproachUnsafeFile),
		Context:   must(agent.SurfaceContext, ApproachSystemPrompt).(*systemPromptContext),
		Hook:      must(agent.SurfaceContext, agent.ApproachHook),
		MCP:       must(agent.SurfaceMCP, ApproachMCPConfig).(*mcpConfig),
		MCPUnsafe: must(agent.SurfaceMCP, agent.ApproachUnsafeFile).(*mcpUnsafeFile),
		Settings:  must(agent.SurfaceSettings, agent.ApproachUnsafeFile).(*settingsSurface),
		Commands:  must(agent.SurfaceCommands, agent.ApproachUnsafeFile).(*commandsSurface),
		Skills:    must(agent.SurfaceSkills, agent.ApproachUnsafeFile),
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
	cwd, home := t.TempDir(), t.TempDir()
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.Context.Deliver(runRoots(cwd, home))
	require.NoError(t, err)

	path := s.Context.Path()
	require.NotEmpty(t, path, "Path() exposes the framed file for --append-system-prompt-file")
	assert.Equal(t, home, filepath.Dir(path), "the framed file lands beneath the relocated engine home")
	assert.True(t, strings.HasSuffix(path, agent.SCMFramedContextSuffix))
	require.FileExists(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, agent.FrameProjectContext(sampleInputs().Context), string(data))

	// The cwd receives nothing, and the home holds no CLAUDE.md.
	assert.NoFileExists(t, filepath.Join(cwd, "CLAUDE.md"))
	assert.NoFileExists(t, filepath.Join(home, "CLAUDE.md"))

	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, path)
}

// ---- MCP surface -----------------------------------------------------------

// MCP Delivery writes the merged .mcp.json (via fileTemplateDelivery) into the
// target dir; Cleanup reverts the ctxloom-owned servers.
func TestMCPSurface_DeliverWritesMCPJSON(t *testing.T) {
	dir := t.TempDir()
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.MCPUnsafe.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	servers := mcpServersOf(t, dir)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")
	assert.Contains(t, servers, AppMCPServerName)

	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, dir)
	assert.NotContains(t, servers, AppMCPServerName, "cleanup reverts ctxloom servers")
}

// The DEFAULT mcp approach writes .mcp.json beneath the private root and
// exposes that path for --mcp-config; the shared cwd is left untouched.
func TestMCPSurface_DeliverWritesPrivateConfig(t *testing.T) {
	cwd := t.TempDir()  // the "shared cwd" — must stay clean
	home := t.TempDir() // the session home — the private root
	s := newSurfaces(sampleInputs(), nil)

	handle, err := s.MCP.Deliver(runRoots(cwd, home))
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(home, ".mcp.json"), s.MCP.Path(),
		"Path() is the private .mcp.json for --mcp-config, beneath the engine home")
	require.FileExists(t, s.MCP.Path())
	assert.NoFileExists(t, filepath.Join(cwd, ".mcp.json"), "the shared cwd is never written")

	servers := mcpServersOf(t, home)
	assert.Contains(t, servers, AppMCPServerName)

	require.NoError(t, handle.Cleanup())
}

// TestMCPSurface_DeliverMaterializesConfigWithNoServers: a run that registers
// NO MCP servers still gets a file on disk.
//
// Present announces --mcp-config unconditionally, and claude REFUSES to start
// against a path that does not exist ("Invalid MCP configuration: MCP config
// file not found"), exiting before it emits anything. That reaches the caller
// as an EMPTY ANSWER, which is indistinguishable from a dead or
// unauthenticated engine — `ctxloom init`'s auth probe reported exactly that,
// and sent the user to re-run `claude login` on working credentials.
//
// The merge alone does not guarantee the file: an empty server set records no
// edits, so the confpatch store writes nothing and reports SUCCESS. This is a
// payload assertion on the file, not on Deliver's error, because that success
// is precisely what made the defect invisible.
func TestMCPSurface_DeliverMaterializesConfigWithNoServers(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()

	in := sampleInputs()
	in.BundleMCP = nil
	s := newSurfaces(in, nil)

	handle, err := s.MCP.Deliver(runRoots(cwd, home))
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(home, ".mcp.json"), s.MCP.Path(),
		"the announced --mcp-config path is the private .mcp.json beneath the engine home")
	require.FileExists(t, s.MCP.Path(),
		"claude refuses to start against a --mcp-config naming a file that does not exist")

	data, err := os.ReadFile(s.MCP.Path())
	require.NoError(t, err)
	var doc agent.ChatMCPConfigDoc
	require.NoError(t, json.Unmarshal(data, &doc),
		"the materialized file must be a valid MCP config document, not an empty or partial one")

	assert.NoFileExists(t, filepath.Join(cwd, ".mcp.json"), "the shared cwd is never written")

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
	fileperm.Equal(t, 0o755, info.Mode(), "the exec bit on scripts/run.sh survives claude's skills surface")

	assert.NoFileExists(t, filepath.Join(dir, ".claude", "skills", "disabled-skill", "SKILL.md"),
		"a skill with Enabled == false must not be written")

	require.NoError(t, handle.Cleanup())
	assert.FileExists(t, skillMD, "a delivered skill package persists after the run (SurfacePersistsAfterExit)")
}

// ---- cells wiring (the vertical slice) -------------------------------------

// ---- the declaration ---------------------------------------------------------

// Surfaces pins claude's per-surface declaration: context offers all three
// approaches (native file, system prompt, settings-carried hook); settings
// offers the project file and the engine-home record write; MCP offers the
// private config file and the project file; commands/skills offer only the
// native file.
//
// The DEFAULTS are the load-bearing half. MCP's is the PRIVATE form, and it is
// the one surface whose default is not the native file: delivering ctxloom's
// MCP set by writing the user's project .mcp.json is a shared/dangerous avenue,
// so it must be asked for by name. Context's default stays the native file —
// a shared launch derives the system prompt instead, which is a preference, not
// a declaration.
func TestSurfaces_DeclaresContextThreeWaysMCPTwoSettingsTwoAndTheRestOnce(t *testing.T) {
	assert.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachSystemPrompt, agent.ApproachHook},
		testDeclaration().Names(agent.SurfaceContext))
	assert.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachHewRecord}, testDeclaration().Names(agent.SurfaceSettings))
	assert.ElementsMatch(t, []string{agent.ApproachUnsafeFile, ApproachMCPConfig}, testDeclaration().Names(agent.SurfaceMCP))
	for _, kind := range []agent.SurfaceKind{agent.SurfaceCommands, agent.SurfaceSkills} {
		assert.Equal(t, []string{agent.ApproachUnsafeFile}, testDeclaration().Names(kind), "%s", kind)
	}

	mcpDef, ok := testDeclaration().Default(agent.SurfaceMCP)
	require.True(t, ok)
	assert.Equal(t, ApproachMCPConfig, mcpDef,
		"the project .mcp.json must never be the default — it is reachable only by name")
	for _, kind := range []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills} {
		def, ok := testDeclaration().Default(kind)
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
	assert.True(t, agent.SafeInSharedCwd(s.MCP), "the private mcp config stays out of the workspace")
	assert.False(t, agent.SafeInSharedCwd(s.MCPUnsafe), "mcp:unsafe-file is the project file — honoured, and warned")
	assert.False(t, agent.SafeInSharedCwd(s.Settings), "settings writes the project's .claude/settings.json: honoured, and warned")
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
	assert.True(t, launchOnly(s.MCP), "the private mcp config is announced on a flag, so it has no argv sink at rest")
	for _, a := range []agent.Approach{s.Native, s.Hook, s.MCPUnsafe, s.Settings, s.Commands, s.Skills} {
		assert.False(t, launchOnly(a))
	}
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
// surface-scoped fields (denyTools, selfContainedCommands) as a coupling
// defect because the constructor cannot set them and a missed assignment is
// compile-clean. The assignments are real and each is exactly-once, but
// "compile-clean if missed" is a TEST gap, not a constructor problem — a
// functional-options constructor is just as silently omittable.
//
// So this closes the gap: every such input is driven from SurfaceInputs through
// NewSurfaces and asserted on the delivered PAYLOAD. denyTools was already
// covered by the Setup deny-tools tests; selfContainedCommands had no
// surface-level coverage at all, which is precisely the field whose omission
// would silently drop commands from a portable materialize target.
func TestNewSurfaces_ThreadsEverySurfaceScopedInput(t *testing.T) {
	fakeHome := testsupport.Isolate(t)

	dup := agent.CommandExport{Name: "recover", Content: "Recovering context", Enabled: true}
	writeRenderedHomeCommand(t, fakeHome, dup)

	in := sampleInputs()
	in.Commands = []agent.CommandExport{dup}
	in.SelfContainedCommands = true
	in.DenyTools = []string{"Task"}

	dir := t.TempDir()
	s := newSurfaces(in, nil)

	_, err := s.Commands.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, ".claude", "commands", "recover.md"),
		"SelfContainedCommands must reach the commands writer — otherwise a portable target silently loses "+
			"every command that happens to exist in the delivering machine's home")

	_, err = s.Settings.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)
	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(settingsData), "Task", "DenyTools must reach permissions.deny")
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
	for kind := range testDeclaration() {
		t.Run(kind.String(), func(t *testing.T) {
			dir := t.TempDir()
			def, ok := testDeclaration().Default(kind)
			require.True(t, ok, "%s is declared, so it must have a default", kind)
			a, ok := testDeclaration().Construct(kind, def, sampleInputs(), nil)
			require.True(t, ok)

			// Every root advised: a default approach may root under any of
			// them, and one that roots privately REFUSES an unadvised root
			// rather than falling back to the project file.
			start := runRoots(dir, t.TempDir())
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
	a, ok := testDeclaration().Construct(agent.SurfaceContext, agent.ApproachUnsafeFile, sampleInputs(), nil)
	require.True(t, ok)
	host := a.Present(present.ProjectOnHost("/home/dev/project")).HostPath
	worktree := a.Present(present.ProjectOnHost("/home/dev/worktrees/project--feat")).HostPath
	assert.NotEqual(t, host, worktree)
	assert.Equal(t, filepath.Join("/home/dev/project", ContextFileName), host)
	assert.Equal(t, filepath.Join("/home/dev/worktrees/project--feat", ContextFileName), worktree)
	assert.NotEmpty(t, testDeclaration().Names(agent.SurfaceContext), "enumeration needs no root and no construction")
}

// TestPrivateRootApproaches_RefuseWithoutAnEngineHome is feeble-sway's
// no-fallback condition at the seam. An approach whose ONLY form lands beneath
// the private root, handed a run that advises none, refuses with
// ErrUnrootedSessionHome: it writes neither the project file nor anything
// else, and records no path for a launch flag to name. The tempting
// substitutions — CLAUDE.md for the system prompt, the project .mcp.json for
// the private one — are each a different product
// behaviour under a different name, and the surface is the wrong place to
// pick one.
func TestPrivateRootApproaches_RefuseWithoutAnEngineHome(t *testing.T) {
	project := t.TempDir()
	s := newSurfaces(sampleInputs(), nil)
	noHome := runRoots(project, "")

	for _, tc := range []struct {
		name    string
		deliver func(present.Start) (agent.Delivered, error)
		path    func() string
	}{
		{"context", s.Context.Deliver, s.Context.Path},
		{"mcp", s.MCP.Deliver, s.MCP.Path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handle, err := tc.deliver(noHome)
			require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
			assert.Nil(t, handle, "a refusal holds no cleanup handle: nothing was written")
			assert.Empty(t, tc.path(), "a refusal records no path — no flag may name it")
			assert.Empty(t, dirEntries(t, project), "no fallback to the project file")
		})
	}
}

// dirEntries lists the names directly beneath dir, so a test can assert a
// root received nothing (or nothing of a given shape).
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
