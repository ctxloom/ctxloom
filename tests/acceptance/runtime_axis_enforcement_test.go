//go:build acceptance

// Package acceptance: the CONTROL for task unwatched-discharge's Step 2 —
// an unrecognized or retired `agents.<x>.runtime` value in the config FILE
// must be fatal, never silently accepted. The coordinator's own reproduction
// used `doctor` and `agent list`, neither of which is a startup choke owner
// (doctor's own feature file, cli/doctor.feature, is titled "why its exit
// code is not the verdict" and pins, as tested behaviour, that NO check
// content ever changes doctor's exit code — warn IS its fail-loud signal).
// `run --dry-run` is the hermetic control instead: it needs no engine
// credentials, and internal/cli/run.go's runRun calls gateStartup() (gate 1,
// which is where config.go's schema-validation warnings become a fatal
// ClassConfig finding — internal/config/warnings.go's
// WarningKind.StrictnessClass) BEFORE the --dry-run early return, so gate 1
// still fires even though no engine is ever spawned.
package acceptance

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/config"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestRuntimeAxis_ConfigFileControl is the exit-code control: a valid runtime
// axis value must let `run --dry-run` proceed (exit 0), while a retired or
// entirely unknown value must abort it (non-zero) in the SAME harness. This
// asserts ONLY the exit code and that the failing rows differ from the
// passing one — never message text, per the coordinator's instruction.
func TestRuntimeAxis_ConfigFileControl(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime string
	}{
		{"valid", "container-rootless"},
		{"retired", "container"},
		{"garbage", "utter-nonsense"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := testenv.NewTestEnvironment()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, env.Cleanup()) })
			require.NoError(t, env.Setup())
			require.NoError(t, env.InitGitRepo())
			require.NoError(t, env.CreateProjectConfig())
			require.NoError(t, env.WriteFile(".ctxloom/config.yaml",
				fmt.Sprintf("version: %d\nagents:\n  probe:\n    runtime: %s\n", config.CurrentConfigVersion, tc.runtime)))

			_ = env.Run("run", "--agent", "probe", "--dry-run", "hi")
			exit := env.LastExitCode()

			if tc.name == "valid" {
				assert.Equal(t, 0, exit, "a valid runtime axis must not abort the run")
			} else {
				assert.NotEqual(t, 0, exit, "a %s runtime axis in the config FILE must abort the run, not be silently accepted", tc.name)
			}
		})
	}
}
