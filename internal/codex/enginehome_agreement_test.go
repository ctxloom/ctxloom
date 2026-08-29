//go:build parked_engines

package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
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
// user's real ~/.codex, and the harpless static writers resolve NOTHING AT ALL.
// S7 turned the interim project-root join they briefly shared into a DECLARED
// ABSENCE, so what the second gate below pins is that every one of them
// declines — in its own idiom, and for the one stated reason.

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
	target, why := deliveryHome(runDir)
	require.Equal(t, homeAvailable, why, "a resolved instance is deliverable")
	assert.Equal(t, want, cellScopedCodexHome(target), "delivery target (configSurface via deliveryHome)")
	assert.Equal(t, filepath.Join(want, ConfigFileName), (&CodexHookWriter{}).settingsPathIn(runDir),
		"delivery target (settingsPathIn)")
}

// TestCodexHome_HarplessStaticWritersAllDecline is gate (b)'s in-package half,
// and what survives of the old agreement gate. The static apply/materialize
// path has no session, so it cannot name an instance — and since S7 it does not
// pretend to. Every harpless writer declines, each in the idiom its own
// signature allows, and each quoting the ONE declared reason.
//
// AGREEMENT IS STILL THE CLAIM. Four writers that decline for four different
// reasons, or three that decline while a fourth quietly writes, is the same
// split-writer bug the old gate existed to catch — it just shows up now as a
// path that exists again rather than as two paths disagreeing.
func TestCodexHome_HarplessStaticWritersAllDecline(t *testing.T) {
	const workDir = "/proj"

	assert.Empty(t, (&CodexHookWriter{}).SettingsPath(workDir),
		"static writer (CodexHookWriter.SettingsPath) must name NO file")

	writeErr := (&CodexHookWriter{}).WriteSettings(&wire.HooksConfig{}, nil, workDir)
	require.Error(t, writeErr, "static writer (CodexHookWriter.WriteSettings) must refuse")
	assert.Contains(t, writeErr.Error(), LaunchOnlySettingsReason,
		"the refusal must quote the declared reason, not invent a second one")

	_, registrarErr := (MCPRegistrar{}).ConfigPath(workDir, false)
	require.Error(t, registrarErr, "static writer (MCPRegistrar.ConfigPath, project scope) must refuse")
	assert.Contains(t, registrarErr.Error(), LaunchOnlySettingsReason)

	_, why := deliveryHome("")
	assert.Equal(t, homeLaunchOnly, why,
		"static surface delivery (deliveryHome with no homeOverride) must refuse as launch-only")

	status, err := (&CodexHookWriter{}).Status(workDir)
	require.NoError(t, err, "a status read is a question, and 'nothing here' answers it")
	assert.Equal(t, agent.SettingsStatus{}, status, "there is no project-keyed settings state to report")
}

// TestCodexHome_StaticSurfaceDeliveryWritesNothingHomeKeyed is the declared
// absence asserted on BYTES rather than on strings: the static
// materialize/apply path (NewSurfaces with no homeOverride — registry.go's
// closure) is driven for real, and NOTHING home-keyed may appear anywhere
// beneath the delivery dir.
//
// The walk is the point. Asserting "not at <projectDir>/.codex" would pass a
// regression that moved the fallback one directory sideways; a whole-tree walk
// for the three home-keyed artifacts passes only if none of them was written at
// all. The cwd-keyed surfaces are asserted present by the sibling test below,
// so this is not a vacuous "nothing was written" claim.
func TestCodexHome_StaticSurfaceDeliveryWritesNothingHomeKeyed(t *testing.T) {
	fs := afero.NewMemMapFs()
	const workDir = "/proj"

	s := NewSurfaces(sampleInputs(), "", "", fs)
	for _, d := range []agent.Delivery{s.Config, s.Commands, s.Skills} {
		_, err := d.Deliver(workDir)
		require.NoError(t, err, "a declared absence is a SKIP, not an error: materialize must keep going")
	}
	// The config surface reports the skip in the strongest form available to it,
	// a NIL handle: there is nothing to revert. Its two siblings ride the shared
	// manifest-scoped delivery, which returns a handle whose revert is itself a
	// no-op — so for them the byte walk below is the whole claim.
	delivered, err := s.Config.Deliver(workDir)
	require.NoError(t, err)
	assert.Nil(t, delivered, "nothing was delivered, so there is no handle to clean up")

	var written []string
	require.NoError(t, afero.Walk(fs, "/", func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil //nolint:nilerr // a walk error on a memfs is not this test's subject
		}
		written = append(written, path)
		return nil
	}))
	for _, path := range written {
		for _, forbidden := range []string{ConfigFileName, PromptsDirName, SkillsDirName} {
			assert.NotContains(t, path, forbidden,
				"the harpless static path wrote a home-keyed artifact at %s; it has no home to write one into", path)
		}
	}
}

// TestCodexHome_StaticContextSurfacesStillDeliver is the vacuity guard for the
// test above, and the answer to "did S7 just gut materialize for codex?". No:
// codex's two CWD-keyed surfaces are untouched by the declaration and still
// deliver on the harpless path, which is why `profile materialize --backend
// codex` is NARROWED rather than emptied.
func TestCodexHome_StaticContextSurfacesStillDeliver(t *testing.T) {
	fs := afero.NewMemMapFs()
	const workDir = "/proj"

	s := NewSurfaces(sampleInputs(), "", "", fs)
	for _, d := range []agent.Delivery{s.AgentsMD, s.Context} {
		_, err := d.Deliver(workDir)
		require.NoError(t, err)
	}

	data, err := afero.ReadFile(fs, filepath.Join(workDir, AgentsMDFile))
	require.NoError(t, err, "AGENTS.md is cwd-keyed and must still be written")
	assert.NotEmpty(t, data, "an empty AGENTS.md is the silent no-op this suite exists to catch")
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
