package cli

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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
