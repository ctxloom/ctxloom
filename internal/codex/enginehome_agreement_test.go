package codex

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The writer-agreement gate split in two when the engine home became a
// PER-SESSION instance, because the two halves stopped being the same claim:
//
//	(a) WITHIN ONE RUN, every site that resolves codex's home for one
//	    (workDir, harp) resolves the SAME directory — Setup's delivery target,
//	    Execute's env, and the trust pre-seed. This file's first test.
//	(b) NO HARPLESS CALLER resolves an instance path at all. That one cannot be
//	    asserted from inside this package (it is a statement about the whole
//	    tree), so it lives in tests/arch —
//	    TestArch_SessionHomeResolversRequireHarp.
//
// The old single gate pinned run path and static writers to ONE root. That
// claim is now false ON PURPOSE: a run resolves a per-session instance or the
// user's real ~/.codex, and the harpless static writers resolve neither. What
// remains true, and is pinned below, is that the harpless writers agree with
// EACH OTHER.

// TestCodexHome_RunPathAgreesWithinOneRun is gate (a). b.resolvedProjectDir
// makes it true by construction — Setup computes the resolution once and
// Execute reads the stored value — and this is what keeps that construction
// from being quietly replaced by two independent derivations, which is exactly
// the bug (`CODEX_HOME` silently overridden after delivery) the single-owner
// fix closed.
func TestCodexHome_RunPathAgreesWithinOneRun(t *testing.T) {
	const workDir = "/proj"
	instance, err := SessionHome(workDir, "ugly-icy-squid")
	require.NoError(t, err)
	want := filepath.Join(instance, ConfigDirName)

	env := map[string]string{CodexHomeEnv: want}

	runDir, source := resolveCodexProjectDir(env, workDir, agent.CellKindShared)
	require.Equal(t, codexHomeIsolationProvided, source,
		"the per-session instance reaches codex as an already-prepared CODEX_HOME")
	assert.Equal(t, want, cellScopedCodexHome(runDir), "run path (resolveCodexProjectDir + cellScopedCodexHome)")

	b := NewCodex()
	b.resolvedProjectDir = runDir
	assert.Equal(t, want, b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: workDir, Env: env})[CodexHomeEnv],
		"run path (cellCodexHomeEnv — the env the child is spawned with)")

	// And the surfaces, which are handed the SAME resolution as homeOverride.
	assert.Equal(t, want, cellScopedCodexHome(codexHomeUnder(workDir, runDir)),
		"delivery target (configSurface via codexHomeUnder)")
	assert.Equal(t, filepath.Join(want, ConfigFileName), (&CodexHookWriter{}).settingsPathIn(runDir),
		"delivery target (settingsPathIn)")
}

// TestCodexHome_HarplessStaticWritersAgree is what survives of the old gate.
// The static apply/materialize path has no session, so it cannot name an
// instance; every harpless writer therefore shares the S7-interim project-root
// join, and they must share it with each other — one name for one file is what
// makes the SettingsWriter conformance suite's write-then-read-back mean
// anything. S7 replaces this with a DECLARED ABSENCE
// (launchOnlySettingsReason); until then, drift between these four is still a
// silent split-writer bug.
func TestCodexHome_HarplessStaticWritersAgree(t *testing.T) {
	const workDir = "/proj"
	want := ProjectHome(workDir)
	require.Equal(t, filepath.Join(workDir, ConfigDirName), want,
		"the harpless join is the project root's own .codex — no run resolves here")

	assert.Equal(t, filepath.Join(want, ConfigFileName), (&CodexHookWriter{}).SettingsPath(workDir),
		"static writer (CodexHookWriter.SettingsPath)")

	registrarPath, err := (MCPRegistrar{}).ConfigPath(workDir, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(want, ConfigFileName), registrarPath, "static writer (MCPRegistrar.ConfigPath)")

	assert.Equal(t, want, cellScopedCodexHome(codexHomeUnder(workDir, "")),
		"static surface delivery (codexHomeUnder with no homeOverride)")
}

// TestCodexHome_StaticSurfaceDeliveryLandsWhereTheStaticWriterSays is the
// agreement asserted on BYTES rather than on strings: the static
// materialize/apply path (NewSurfaces with no homeOverride — registry.go's
// closure) is driven for real and every artifact it produces must land under
// the path the static writer names. A path assertion can agree with a writer
// that never wrote; this one cannot.
func TestCodexHome_StaticSurfaceDeliveryLandsWhereTheStaticWriterSays(t *testing.T) {
	fs := afero.NewMemMapFs()
	const workDir = "/proj"
	home := ProjectHome(workDir)

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
		assert.True(t, exists, "the static path delivered %s somewhere other than %s", rel, home)
	}

	// And NOT into the retired durable per-project home, which is what a
	// half-reverted site would leave behind.
	retired, err := afero.DirExists(fs, filepath.Join(workDir, ".ctxloom", "state", "engines"))
	require.NoError(t, err)
	assert.False(t, retired, "no surface may write the retired durable per-project engine home")
}

// TestCodexHome_CwdKeyedSurfacesStayAtTheProjectRoot is the policy's other
// half, and the one an over-eager relocation would break: AGENTS.md and the
// context cache are read by codex (and by the SessionStart hook) FROM THE CWD.
// Moving them under an instance would deliver context nothing reads — visible
// only as a model that mysteriously knows nothing about the project.
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
