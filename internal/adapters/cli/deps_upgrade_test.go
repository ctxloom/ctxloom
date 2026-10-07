package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// `deps upgrade` is DESTRUCTIVE: it re-resolves the dependency closure and
// writes the result straight over the active lock. It used to load its config
// through loadConfigOrFallback — a helper written for the fault-tolerant
// READ-ONLY startup paths (`deps check`, `search`), which hands back a
// minimal EMPTY config on any load error. An empty config has no profile
// definitions, so the closure came out empty and the wholesale write erased
// every pin and hold while printing "Everything is up to date."
//
// A destructive command must not run on a config it could not read.
func TestRemoteUpgrade_RefusesToRunOnAnUnloadableConfig(t *testing.T) {
	loadErr := errors.New("config.yaml: yaml: line 4: did not find expected key")

	err := runDepsUpgrade(&cobra.Command{}, func() (*config.Config, error) {
		return nil, loadErr
	})

	require.Error(t, err, "upgrade must refuse to rewrite the lockfile from a config it could not load")
	assert.ErrorIs(t, err, loadErr, "the underlying config error is reported, not swallowed")
	assert.Contains(t, err.Error(), "upgrade",
		"the error says which operation refused, so the exit code is diagnosable")
}

// An EMPTY resolved closure is not "up to date" — it means nothing is declared
// where the command was run, which is exactly the fact a user who typed
// `deps upgrade` in the wrong directory needs to be told. Printing the same
// cheerful line for both makes the two indistinguishable, which is this
// project's characteristic silent no-op in its reporting half.
func TestRemoteUpgrade_NothingDeclaredIsNotReportedAsUpToDate(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	var err error
	out := captureStdout(t, func() {
		err = runDepsUpgrade(cmd, func() (*config.Config, error) {
			return config.NewFixture(config.Fixture{AppPaths: []string{baseDir}}), nil
		})
	})

	require.NoError(t, err, "nothing declared is not a failure — there is simply nothing here")
	assert.Contains(t, out, msgNothingDeclared,
		"the user is told nothing is declared, and what to do about it")
	assert.NotContains(t, out, msgEverythingUpToDate,
		"'up to date' claims a check that had nothing to check")
}

// A removal is named, by identity, one line each: the lock is rewritten
// wholesale, and an unnamed removal reads as a pin that never existed.
func TestRemoteUpgrade_ReportsEachRemovedPinByName(t *testing.T) {
	out := captureStdout(t, func() {
		reportRemovedPins(os.Stdout, []string{"ctxloom+git://github.com/o/r//bundles/a", "ctxloom+git://github.com/o/r//bundles/b"}, true)
	})
	assert.Equal(t,
		"Removed ctxloom+git://github.com/o/r//bundles/a from the lockfile: nothing this project composes depends on it any more.\n"+
			"Removed ctxloom+git://github.com/o/r//bundles/b from the lockfile: nothing this project composes depends on it any more.\n",
		out)

	preview := captureStdout(t, func() {
		reportRemovedPins(os.Stdout, []string{"ctxloom+git://github.com/o/r//bundles/a"}, false)
	})
	assert.Equal(t, "Would remove ctxloom+git://github.com/o/r//bundles/a from the lockfile: nothing this project composes depends on it any more.\n", preview)
}

func upgradeChange() operations.PinChange {
	return operations.PinChange{Identity: "corp/kit", FromSHA: "1111111111", ToSHA: "2222222222", FromVersion: "v1.0.0", ToVersion: "v1.1.0",
		Items: []operations.ItemChange{{Kind: "hook", Name: "session_start/0", Change: operations.ChangeModified,
			Exec: &operations.ExecDelta{Before: &operations.ExecSpec{Command: "./a.sh"}, After: &operations.ExecSpec{Command: "./b.sh"}}}}}
}

// Without --yes an upgrade shows each move and says how to apply it.
func TestRenderUpgrade_PreviewShowsEachMoveAndHowToApplyIt(t *testing.T) {
	out := captureStdout(t, func() {
		renderUpgrade(os.Stdout, operations.UpgradeResult{Changes: []operations.PinChange{upgradeChange()}})
	})
	assert.Equal(t, "corp/kit  v1.0.0 -> v1.1.0  (1111111 -> 2222222)\n"+
		"  ~ hook session_start/0\n"+
		"      command: ./a.sh -> ./b.sh\n"+
		"1 pin(s) would move. Re-run with --yes to apply.\n", out)
}

// With --yes it shows what it applied.
func TestRenderUpgrade_AppliedSaysWhatItApplied(t *testing.T) {
	out := captureStdout(t, func() {
		renderUpgrade(os.Stdout, operations.UpgradeResult{Applied: true, Changes: []operations.PinChange{upgradeChange()}})
	})
	assert.Contains(t, out, "corp/kit  v1.0.0 -> v1.1.0  (1111111 -> 2222222)\n")
	assert.Contains(t, out, "Applied 1 pin(s).\n")
	assert.NotContains(t, out, "--yes")
}

// A preview that would only drop entries still needs --yes, and says so.
func TestRenderUpgrade_PreviewOfARemovalNamesYes(t *testing.T) {
	out := captureStdout(t, func() {
		renderUpgrade(os.Stdout, operations.UpgradeResult{Removed: []string{"corp/old"}})
	})
	assert.Contains(t, out, "Would remove corp/old from the lockfile")
	assert.Contains(t, out, "Re-run with --yes to apply.")
}
