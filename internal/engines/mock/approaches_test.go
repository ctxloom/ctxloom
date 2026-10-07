package mock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// This file proves the mock engine's typed approaches (surfaces.go) — the
// delivery the static writer drives — and the name table a binding selects
// from (declaration.go). ctxloom's characteristic bug is the SILENT NO-OP:
// exit 0, a success message, zero bytes written. Every delivery assertion
// below is on the delivered BYTES, MODE or PATH, never on an error being nil.

// everyKind is every surface kind a binding can name.
var everyKind = []agent.SurfaceKind{agent.SurfaceContext, agent.SurfaceMCP, agent.SurfaceSettings, agent.SurfaceCommands, agent.SurfaceSkills}

// TestMockDeclaration_DeclaresEveryKind pins mock's declared scope: EVERY
// SurfaceKind is selectable, defaulting to the session form with the project
// form selectable by name, because mock is a complete engine with no real
// model behind it. A partial double makes its gaps load-bearing somewhere
// nothing states them. Every name the table offers is a kind the Definition
// actually delivers.
func TestMockDeclaration_DeclaresEveryKind(t *testing.T) {
	m := New().(Mock)
	decl := m.Declaration()
	for _, kind := range everyKind {
		assert.Equal(t, []string{MockSessionFile, agent.ApproachUnsafeFile}, decl.Names(kind), "kind %v", kind)
		def, ok := decl.Default(kind)
		require.True(t, ok, "%v must have a default", kind)
		assert.Equal(t, MockSessionFile, def, "kind %v defaults to the session form (ruled 2026-09-21)", kind)
		assert.True(t, m.Carries(kind), "the table names %v, which the Definition does not deliver", kind)
	}
}

// TestMockDeclaration_AnUndeclaredNameOrKindIsAbsent: claude's system-prompt
// is not one of mock's names, and a kind outside the seam reads as absent —
// no names, no default — rather than fabricating a selectable surface.
func TestMockDeclaration_AnUndeclaredNameOrKindIsAbsent(t *testing.T) {
	decl := New().(Mock).Declaration()
	assert.NotContains(t, decl.Names(agent.SurfaceContext), "system-prompt")

	const notASurface = agent.SurfaceKind(9999)
	assert.Nil(t, decl.Names(notASurface))
	_, ok := decl.Default(notASurface)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// WHERE EACH APPROACH PRESENTS
// ---------------------------------------------------------------------------

// deliverSample delivers kind through the mock's typed approach with a
// non-empty input, over fs.
func deliverSample(t *testing.T, kind present.Kind, start present.Start, root present.RootKind, fs afero.Fs) present.Delivered {
	t.Helper()
	def := New().Root()
	var (
		d   present.Delivered
		err error
	)
	switch kind {
	case present.Context:
		d, err = def.Context.DeliverContext(start, root, engine.ContextInputs{Text: []byte("ctx")}, fs)
	case present.MCP:
		d, err = def.MCP.DeliverMCP(start, root, engine.MCPInputs{Servers: map[string]wire.MCPServer{"s": {Command: "c"}}}, fs)
	case present.Settings:
		d, err = def.Settings.DeliverSettings(start, root, engine.SettingsInputs{DenyTools: []string{"Bash"}}, fs)
	case present.Commands:
		d, err = def.Commands.DeliverCommands(start, root, engine.CommandsInputs{Commands: []engine.CommandExport{{Name: "c", Body: []byte("b"), Enabled: true}}}, safefs.NewMem(fs))
	case present.Skills:
		d, err = def.Skills.DeliverSkills(start, root, engine.SkillsInputs{Skills: []engine.SkillExport{reviewerSkill()}}, safefs.NewMem(fs))
	default:
		t.Fatalf("no sample for %v", kind)
	}
	require.NoError(t, err, "deliver %v at %v", kind, root)
	return d
}

// kindRel is where each kind lands beneath the root it is delivered under,
// stated LITERALLY so a wrong root or rel in surfaces.go cannot agree with
// itself.
var kindRel = map[agent.SurfaceKind]string{
	agent.SurfaceContext:  "MOCK_CONTEXT.md",
	agent.SurfaceMCP:      ".mock/mcp.json",
	agent.SurfaceSettings: ".mock/settings.json",
	agent.SurfaceCommands: ".mock/commands",
	agent.SurfaceSkills:   ".mock/skills",
}

// TestMockApproaches_ProjectRootPresentsUnderTheProject_NotTheSessionHome: at
// the project root every kind presents beneath the project, on a Start whose
// session home is a DIFFERENT non-empty path — a presenter that read the
// wrong root would otherwise pass unseen.
func TestMockApproaches_ProjectRootPresentsUnderTheProject_NotTheSessionHome(t *testing.T) {
	start := present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: "/proj"},
		SessionHome: present.Root{Host: "/elsewhere/home"},
	}))
	for _, kind := range everyKind {
		got := deliverSample(t, kind, start, present.RootProjectRoot, afero.NewMemMapFs()).Presented
		assert.Equal(t, filepath.Join("/proj", filepath.FromSlash(kindRel[kind])), got.HostPath, "kind %v", kind)
	}
}

// TestMockApproaches_SessionHomeKeepsBothSides: at the session home every kind
// presents beneath the session home on BOTH sides. Where an environment
// relocated that home (a container's $HOME), the engine must be told the
// Engine side — the host path does not exist where it runs.
func TestMockApproaches_SessionHomeKeepsBothSides(t *testing.T) {
	start := present.New(present.Advised(present.Paths{
		ProjectRoot: present.Root{Host: "/proj", Engine: "/proj"},
		SessionHome: present.Root{Host: "/sessions/harp/home/mock", Engine: "/home/ctxloom"},
	}))
	for _, kind := range everyKind {
		got := deliverSample(t, kind, start, present.RootSessionHome, afero.NewMemMapFs()).Presented
		assert.Equal(t, filepath.Join("/sessions/harp/home/mock", filepath.FromSlash(kindRel[kind])), got.HostPath, "kind %v: the bytes land on the host side", kind)
		assert.Equal(t, "/home/ctxloom/"+kindRel[kind], got.EnginePath, "kind %v: the engine is told the Engine side", kind)
	}
}

// TestMockApproaches_ContainerizedProjectRoot_EnginePathDivergesFromHostPath:
// against a Start whose project root an environment relocated, the engine
// path lands at the container-visible root while the bytes land on the host.
func TestMockApproaches_ContainerizedProjectRoot_EnginePathDivergesFromHostPath(t *testing.T) {
	start := present.New(present.Advised(present.Paths{
		ProjectRoot: present.Root{Host: "/home/user/project", Engine: "/mnt/proj"},
	}))
	got := deliverSample(t, present.Context, start, present.RootProjectRoot, afero.NewMemMapFs()).Presented
	assert.Equal(t, filepath.Join("/home/user/project", ContextFileName), got.HostPath)
	assert.Equal(t, "/mnt/proj/"+ContextFileName, got.EnginePath)
}

// ---------------------------------------------------------------------------
// THE SKILLS PAYLOAD
//
// A skills delivery that reports success and materializes an empty tree — or
// a tree whose script is not executable and therefore cannot run — is the
// exact silent no-op this project keeps producing.
// ---------------------------------------------------------------------------

// reviewerSkill is the fixture package: SKILL.md plus an executable script,
// the shape J001400's delivery matrix asserts. The modes are the export's
// DECLARATION — on the real path they arrive from the package sidecar's
// `executable:` list through the signed manifest, never from a stat — so the
// assertions below are on the declaration reaching disk.
func reviewerSkill() engine.SkillExport {
	return engine.SkillExport{
		Name: "reviewer", Description: "the fixture skill", Enabled: true,
		Files: []engine.SkillFile{
			{Path: "SKILL.md", Bytes: []byte("MOCK-SKILL-BODY-2c7e"), Mode: 0o644},
			{Path: "scripts/run.sh", Bytes: []byte("#!/bin/sh\necho MOCK-SCRIPT-9a41\n"), Mode: 0o755},
		},
	}
}

// deliverSkills delivers skills at the project root dir over fs.
func deliverSkills(t *testing.T, fs afero.Fs, dir string, skills ...engine.SkillExport) present.Delivered {
	t.Helper()
	d, err := New().Root().Skills.DeliverSkills(present.ProjectOnHost(dir), present.RootProjectRoot, engine.SkillsInputs{Skills: skills}, safefs.NewMem(fs))
	require.NoError(t, err)
	return d
}

// TestMockSkills_Deliver_WritesEveryFileWithItsBytes: each file of the
// package lands with its own content under the skill's own directory, and
// the delivery declares each one. A tree of empty files would pass a "path
// exists" check and fail this one.
func TestMockSkills_Deliver_WritesEveryFileWithItsBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	d := deliverSkills(t, fs, "/target", reviewerSkill())

	base := filepath.Join("/target", ".mock", "skills", "reviewer")
	skillMD, err := afero.ReadFile(fs, filepath.Join(base, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "MOCK-SKILL-BODY-2c7e", string(skillMD))
	script, err := afero.ReadFile(fs, filepath.Join(base, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Contains(t, string(script), "MOCK-SCRIPT-9a41", "a sibling file in a subdirectory is delivered too")
	assert.ElementsMatch(t, []string{filepath.Join(base, "SKILL.md"), filepath.Join(base, "scripts", "run.sh")}, d.Files)
}

// TestMockSkills_Deliver_MaterializesTheDeclaredMode: a delivered script that
// is not executable is a package that reports success and cannot run, and a
// SKILL.md delivered executable is the declaration ignored the other way.
func TestMockSkills_Deliver_MaterializesTheDeclaredMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	deliverSkills(t, fs, "/target", reviewerSkill())

	base := filepath.Join("/target", ".mock", "skills", "reviewer")
	for rel, want := range map[string]os.FileMode{"scripts/run.sh": 0o755, "SKILL.md": 0o644} {
		info, err := fs.Stat(filepath.Join(base, filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.Equal(t, want, info.Mode().Perm(), "%s is DECLARED %o", rel, want)
	}
}

// TestMockSkills_Deliver_DeclaredModeBeatsAnExistingFilesMode: a previous
// delivery left run.sh non-executable; re-delivering the declared-executable
// export must correct it rather than keep the mode already on disk.
func TestMockSkills_Deliver_DeclaredModeBeatsAnExistingFilesMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	scriptPath := filepath.Join("/target", ".mock", "skills", "reviewer", "scripts", "run.sh")
	testsupport.WriteFile(t, fs, scriptPath, []byte("stale\n"), 0o600)

	deliverSkills(t, fs, "/target", reviewerSkill())

	got, err := fs.Stat(scriptPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), got.Mode().Perm(), "the DECLARED mode wins over the mode the file already had")
}

// TestMockSkills_Deliver_DisabledSkillWritesNothing: a skill the export marks
// disabled leaves no tree and declares nothing — "err == nil" alone would
// pass even if the package were materialized anyway.
func TestMockSkills_Deliver_DisabledSkillWritesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	off := reviewerSkill()
	off.Enabled = false
	d := deliverSkills(t, fs, "/target", off)

	exists, err := afero.DirExists(fs, filepath.Join("/target", ".mock", "skills"))
	require.NoError(t, err)
	assert.False(t, exists, "a disabled skill writes NOTHING — not even an empty skills directory")
	assert.Empty(t, d.Files, fmt.Sprintf("a disabled skill is not declared: %v", d.Files))
}
