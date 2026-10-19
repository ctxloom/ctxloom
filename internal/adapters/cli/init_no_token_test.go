package cli

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestRunInit_CredentialMissing_FailsWhateverTheMode: init that has set the
// project up but finds no agent credential exits non-zero with the engine's
// own fix — the one a run is refused with — under --skip-launch and off a
// terminal alike, and says the config WAS written. A script reading exit 0
// there was told a project it cannot run is ready. With the token exported
// init succeeds.
func TestRunInit_CredentialMissing_FailsWhateverTheMode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		skipLaunch bool
		token      string
		wantErr    bool
	}{
		{"skip-launch, no token", true, "", true},
		{"off a terminal, no token", false, "", true},
		{"skip-launch, token exported", true, "not-a-real-token", false},
		{"off a terminal, token exported", false, "not-a-real-token", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testsupport.Isolate(t)
			project := t.TempDir()
			chdir(t, project)
			resetApp()
			t.Cleanup(resetApp)
			withInitFlags(t, true)
			initSkipLaunch = tc.skipLaunch
			t.Setenv(claude.OAuthTokenEnv, tc.token)
			origClone := cloneConfiguredRemotesFn
			cloneConfiguredRemotesFn = func(*cobra.Command, string) {}
			t.Cleanup(func() { cloneConfiguredRemotesFn = origClone })

			var err error
			captureStdout(t, func() { err = runInit(initTestCmd(), nil) })

			assert.FileExists(t, filepath.Join(project, ".ctxloom", "config.yaml"), "the config stays written")
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrAgentTokenNotExported)
			assert.NotZero(t, clifmt.ExitCodeOf(err))
			assert.Contains(t, err.Error(), "config is written")
			assert.Contains(t, err.Error(), claude.OAuthTokenEnv)
			want, _ := clifmt.RemedyOf(operations.CheckRunCredential(engines.Registry(), claude.EngineName, engine.AuthToken))
			got, _ := clifmt.RemedyOf(err)
			assert.Equal(t, want, got, "init's fix is the run's")
		})
	}
}
