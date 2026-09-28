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

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// `deps upgrade` is DESTRUCTIVE: it re-resolves the dependency closure and
// writes the result straight over the active lock. It used to load its config
// through loadConfigOrFallback — a helper written for the fault-tolerant
// READ-ONLY startup paths (`deps check`, `search`), which hands back a
// minimal EMPTY config on any load error. An empty config has no profile
// definitions, so the closure came out empty and the wholesale write erased
// every pin, hold and retraction while printing "Everything is up to date."
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
		reportRemovedPins([]string{"ctxloom+git://github.com/o/r//bundles/a", "ctxloom+git://github.com/o/r//bundles/b"})
	})
	assert.Equal(t,
		"Removed ctxloom+git://github.com/o/r//bundles/a from the lockfile: nothing this project composes depends on it any more.\n"+
			"Removed ctxloom+git://github.com/o/r//bundles/b from the lockfile: nothing this project composes depends on it any more.\n",
		out)
}
