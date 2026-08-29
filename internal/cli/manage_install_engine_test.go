package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// parked_engines: internal/codex is out of the default build;
	// TestManageInstall_EngineScopesWrites, the one test that needed its
	// AGENTS.md/declared-absence shape, is commented out with it.
	// "github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// runCLIErr executes rootCmd with args and returns everything written to the
// command's out/err streams plus the error. Unlike runCLIJSON it does not
// require success — the point is usually that a command must NOT succeed.
func runCLIErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return out.String(), err
}

// TestManageInstall_EngineOnExistingDirIsNotSilentlyDropped pins that
// `--engine` was read only inside the `if !ctxloomDirExists(appDir)`
// branch, so re-running install to change the recorded engine printed the
// normal success lines and exited 0 while the config kept the old engine —
// a user-visible instruction ("re-run install with --engine") that did nothing.
//
// The payload assertion is the one that matters: the recorded engine must be
// unchanged, and the command must say so rather than reporting success.
func TestManageInstall_EngineOnExistingDirIsNotSilentlyDropped(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	_, err := runCLIErr(t, "manage", "install", "--print=false", "--engine", "claude-code")
	require.NoError(t, err, "first install scaffolds")

	cfgPath := filepath.Join(dir, ".ctxloom", "config.yaml")
	before, rerr := os.ReadFile(cfgPath)
	require.NoError(t, rerr)

	// "mock" stands in for "a second registered engine distinct from
	// claude-code" — codex would have too, but it is parked (parked_engines)
	// and checkEngineKnown now refuses it before this test's target check
	// (checkInstallEngineApplies) is ever reached, which would pass this
	// test for the wrong reason.
	_, err = runCLIErr(t, "manage", "install", "--print=false", "--engine", "mock")
	require.Error(t, err, "a --engine that cannot be applied must not report success")
	assert.Contains(t, err.Error(), "mock", "the refusal must name the engine that was asked for")

	after, rerr := os.ReadFile(cfgPath)
	require.NoError(t, rerr)
	assert.Equal(t, string(before), string(after), "the rejected install must change nothing")
}

// TestManageInstall_RerunWithoutEngineStillWorks is the guard on the fix: only
// an EXPLICIT --engine on an already-scaffolded project is refused. Re-running
// `manage install` to re-apply hooks is the documented way to repair a harness
// and must keep working, including when the flag sits at its default value.
func TestManageInstall_RerunWithoutEngineStillWorks(t *testing.T) {
	testsupport.ProjectDir(t)

	_, err := runCLIErr(t, "manage", "install", "--print=false", "--engine", "claude-code")
	require.NoError(t, err)

	// pflag records Changed on the shared FlagSet and cobra never resets it
	// across repeated Execute() calls on one command tree (the same artefact
	// manage_format_test.go documents for --print). Clear it so this call is
	// the "no --engine passed" invocation a real second process would make.
	require.NoError(t, manageInstallCmd.Flags().Set("engine", "claude-code"))
	manageInstallCmd.Flags().Lookup("engine").Changed = false

	_, err = runCLIErr(t, "manage", "install", "--print=false")
	require.NoError(t, err, "re-running install to re-apply hooks must stay a supported repair")
}

// TestManageInstall_UnknownEngineRefusesLoud pins the fix for the "exit 0 +
// success message + corrupt config" defect: `manage install --engine bogus`
// used to print "Initialized ctxloom directory" and write a config.yaml that
// then failed ctxloom's own JSON-schema validation on the very next command.
// It must instead refuse loud, name the offending value, list the valid set,
// and leave NOTHING behind — not even an empty .ctxloom directory.
func TestManageInstall_UnknownEngineRefusesLoud(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	out, err := runCLIErr(t, "manage", "install", "--print=false", "--engine", "bogus")
	require.Error(t, err, "an unknown engine must not report success")
	assert.Contains(t, err.Error(), `"bogus"`, "the refusal must name the offending value")
	assert.Contains(t, err.Error(), "claude-code", "the refusal must list the valid engine set")
	assert.NotContains(t, out, "Initialized ctxloom directory", "no success line on a rejected engine")

	_, statErr := os.Stat(filepath.Join(dir, ".ctxloom"))
	assert.True(t, os.IsNotExist(statErr), "a rejected engine must leave no .ctxloom directory behind")
}

// TestManageInstall_EngineScopesWrites pins the fix for the second defect on
// the same flag: `--engine codex` used to write EVERY registered engine's
// surfaces (.claude/, .kiro/, .opencode/, .agents/ all materializing in a
// project that uses only codex) because ApplyHooks was always called with
// Backend: "all", ignoring the flag entirely except for the config's
// recorded default. An explicit --engine must scope the hook apply to that
// one backend.
// parked_engines: this test's whole point is codex's declared-absence
// scoping shape (AGENTS.md written, no .codex dir) — internal/codex is out
// of the default build, so there is nothing left for it to prove until it
// returns.
//
// func TestManageInstall_EngineScopesWrites(t *testing.T) {
// 	dir := testsupport.ProjectDir(t)
//
// 	_, err := runCLIErr(t, "manage", "install", "--print=false", "--engine", "codex")
// 	require.NoError(t, err)
//
// 	// codex's ONE static surface is its cwd-keyed AGENTS.md: since S7 its
// 	// home-keyed surfaces are a declared absence, delivered per-session at
// 	// launch and written nowhere by a static install
// 	// (internal/codex/declared_absence.go). Asserting AGENTS.md is what keeps
// 	// this scoping test honest — with no positive assertion at all, an install
// 	// that wrote nothing anywhere would pass the four negatives below.
// 	assert.FileExists(t, filepath.Join(dir, codex.AgentsMDFile), "the named engine's cwd-keyed surface must be written")
// 	assert.NoDirExists(t, filepath.Join(dir, codex.ConfigDirName),
// 		"and no project-root .codex: codex has no durable project home to write one into")
// 	assert.NoDirExists(t, filepath.Join(dir, ".ctxloom", "state", "engines"),
// 		"nor the retired durable per-project engine home")
// 	for _, other := range []string{".claude", ".kiro", ".opencode", ".agents"} {
// 		_, statErr := os.Stat(filepath.Join(dir, other))
// 		assert.True(t, os.IsNotExist(statErr), "%s must NOT be written when --engine codex was asked for", other)
// 	}
// }

// TestManageInstall_NoEngineFlagAppliesAllBackends is the guard on the
// scoping fix: omitting --engine is the documented "wire everything" install
// and must keep writing every backend's surface, exactly as before — the
// flag's ABSENCE, not its default value, is what selects "all".
func TestManageInstall_NoEngineFlagAppliesAllBackends(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	// pflag's Changed sticks on the shared FlagSet across Execute() calls in
	// the same process (TestManageInstall_RerunWithoutEngineStillWorks above
	// documents the same artefact) — reset it so this run is the "no --engine
	// passed" invocation a real process would make, regardless of what an
	// earlier test in this binary left behind.
	require.NoError(t, manageInstallCmd.Flags().Set("engine", "claude-code"))
	manageInstallCmd.Flags().Lookup("engine").Changed = false

	_, err := runCLIErr(t, "manage", "install", "--print=false")
	require.NoError(t, err)

	// parked_engines: kiro/opencode/codex are out of the default build and
	// BackendsWithSettings() no longer names them, so "wire everything" only
	// writes claude's surface until they return.
	for _, backend := range []string{".claude"} {
		assert.DirExists(t, filepath.Join(dir, backend), "omitting --engine must still wire %s", backend)
	}
}

// TestCheckInstallEngineApplies covers the decision itself, free of the cobra
// flag plumbing: only the explicit-flag-plus-existing-dir combination is an
// error, so neither a first install nor a plain re-run is affected.
func TestCheckInstallEngineApplies(t *testing.T) {
	assert.NoError(t, checkInstallEngineApplies(false, true, "codex"), "scaffolding honours --engine")
	assert.NoError(t, checkInstallEngineApplies(false, false, "claude-code"), "scaffolding without the flag uses the default")
	assert.NoError(t, checkInstallEngineApplies(true, false, "claude-code"), "a plain re-run re-applies hooks")
	assert.Error(t, checkInstallEngineApplies(true, true, "codex"), "an --engine that cannot be recorded must fail loud")
}
