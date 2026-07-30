package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestManageInstall_EngineOnExistingDirIsNotSilentlyDropped is the U037-F22
// pin. `--engine` was read only inside the `if !ctxloomDirExists(appDir)`
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

	_, err = runCLIErr(t, "manage", "install", "--print=false", "--engine", "codex")
	require.Error(t, err, "a --engine that cannot be applied must not report success")
	assert.Contains(t, err.Error(), "codex", "the refusal must name the engine that was asked for")

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

// TestCheckInstallEngineApplies covers the decision itself, free of the cobra
// flag plumbing: only the explicit-flag-plus-existing-dir combination is an
// error, so neither a first install nor a plain re-run is affected.
func TestCheckInstallEngineApplies(t *testing.T) {
	assert.NoError(t, checkInstallEngineApplies(false, true, "codex"), "scaffolding honours --engine")
	assert.NoError(t, checkInstallEngineApplies(false, false, "claude-code"), "scaffolding without the flag uses the default")
	assert.NoError(t, checkInstallEngineApplies(true, false, "claude-code"), "a plain re-run re-applies hooks")
	assert.Error(t, checkInstallEngineApplies(true, true, "codex"), "an --engine that cannot be recorded must fail loud")
}
