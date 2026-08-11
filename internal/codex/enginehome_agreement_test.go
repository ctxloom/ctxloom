package codex

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestCodexHome_RunPathAndStaticWritersAgree is the gate that keeps the writer
// split closed.
//
// codex is the one engine whose config home ctxloom RELOCATES, and for a long
// while it relocated it twice: the run path resolved <WorkDir>/.codex through
// resolveCodexProjectDir while every static writer re-derived the same join by
// hand. Nothing failed — each side wrote a perfectly good config.toml — so a
// materialize and a run could target different roots and both report success,
// which is precisely this project's silent-no-op shape wearing a delivery's
// clothes. The moment those two joins stop being literally the same expression
// (and after the engine-home policy they are not: one is StateHome, the other
// was a bare filepath.Join), only an assertion can hold them together.
//
// Every project-scoped site is listed here BY NAME. A new one that forgets to
// route through StateHome is not caught by this test — but a site that is here
// and drifts is, and the doctor/materialize/run trio is the whole reachable set
// today.
func TestCodexHome_RunPathAndStaticWritersAgree(t *testing.T) {
	const workDir = "/proj"
	want := ProjectHome(workDir)

	require.Equal(t, filepath.Join(StateHome(workDir), ConfigDirName), want,
		"ProjectHome must be cellScopedCodexHome under the policy's state home")

	// 1. The RUN path: what the launched codex actually gets as CODEX_HOME.
	runDir, source := resolveCodexProjectDir(nil, workDir, agent.CellKindShared)
	require.Equal(t, codexHomeInTree, source, "no isolation-provided home and no container cell is the in-tree axis")
	assert.Equal(t, want, cellScopedCodexHome(runDir), "run path (resolveCodexProjectDir + cellScopedCodexHome)")

	b := NewCodex()
	b.resolvedProjectDir = runDir
	assert.Equal(t, want, b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: workDir})[CodexHomeEnv],
		"run path (cellCodexHomeEnv — the env the child is spawned with)")

	// 2. The STATIC settings writer (`ctxloom manage install`, backends
	//    registry's newWriter).
	assert.Equal(t, filepath.Join(want, ConfigFileName), (&CodexHookWriter{}).SettingsPath(workDir),
		"static writer (CodexHookWriter.SettingsPath)")

	// 3. The MCP registrar, which folds into the very same file.
	registrarPath, err := (MCPRegistrar{}).ConfigPath(workDir, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(want, ConfigFileName), registrarPath, "static writer (MCPRegistrar.ConfigPath)")

	// 4. The hook-scope collision check the backends registry wires
	//    (hookGlobalScopePaths).
	assert.Equal(t, want, ProjectHome(workDir), "static writer (codex.ProjectHome)")
}

// TestCodexHome_StaticSurfaceDeliveryLandsUnderTheRunPathHome is the agreement
// asserted on BYTES rather than on strings: the static materialize/apply path
// (NewSurfaces with no homeOverride — registry.go's closure) is driven for real
// and every artifact it produces must land under the same home the run path
// resolves. A path assertion can agree with a writer that never wrote; this one
// cannot.
func TestCodexHome_StaticSurfaceDeliveryLandsUnderTheRunPathHome(t *testing.T) {
	fs := afero.NewMemMapFs()
	const workDir = "/proj"
	runDir, _ := resolveCodexProjectDir(nil, workDir, agent.CellKindShared)
	home := cellScopedCodexHome(runDir)

	s := NewSurfaces(sampleInputs(), "", "", fs)
	for _, d := range []agent.Delivery{s.Config, s.Commands, s.Skills} {
		_, err := d.Deliver(workDir)
		require.NoError(t, err)
	}

	for _, rel := range []string{
		ConfigFileName,
		filepath.Join(PromptsDirName, "review.md"),
		filepath.Join(SkillsDirName, "humanize", "SKILL.md"),
	} {
		path := filepath.Join(home, rel)
		exists, err := afero.Exists(fs, path)
		require.NoError(t, err)
		assert.True(t, exists, "the static path delivered %s somewhere other than the run path's CODEX_HOME (%s)", rel, home)
	}

	// And NOT at the pre-relocation location, which is what a half-migrated
	// site would leave behind.
	legacy, err := afero.DirExists(fs, legacyProjectHome(workDir))
	require.NoError(t, err)
	assert.False(t, legacy, "no surface may still write the pre-relocation <WorkDir>/.codex")
}

// TestCodexHome_CwdKeyedSurfacesStayAtTheProjectRoot is the policy's other
// half, and the one an over-eager relocation would break: AGENTS.md and the
// context cache are read by codex (and by the SessionStart hook) FROM THE CWD.
// Moving them under the state home would deliver context nothing reads —
// visible only as a model that mysteriously knows nothing about the project.
func TestCodexHome_CwdKeyedSurfacesStayAtTheProjectRoot(t *testing.T) {
	fs := afero.NewMemMapFs()
	const workDir = "/proj"

	s := NewSurfaces(sampleInputs(), "", "", fs)
	_, err := s.AgentsMD.Deliver(workDir)
	require.NoError(t, err)

	exists, err := afero.Exists(fs, filepath.Join(workDir, AgentsMDFile))
	require.NoError(t, err)
	assert.True(t, exists, "AGENTS.md is cwd-keyed: codex reads it from the project root, and ctxloom does not get a vote")
}
