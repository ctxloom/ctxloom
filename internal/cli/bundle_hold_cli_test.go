package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `bundle hold` / `bundle unhold` on an item that is not in the active lockfile
// achieved NOTHING: no entry was flipped, nothing was persisted. Reporting that
// on stdout and exiting 0 is this project's signature silent no-op — a script
// that holds a mistyped name saw success and an unfrozen dependency. It must
// FAIL, and say what to do about it.
func TestBundleHoldUnhold_UnknownItemFailsInsteadOfNoOpping(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*cobra.Command, []string) error
	}{
		{"hold", bundleHoldCmd.RunE},
		{"unhold", bundleUnholdCmd.RunE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupEditProject(t) // a project with no lockfile at all

			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			err := tc.run(cmd, []string{"not-installed"})

			require.Error(t, err, "nothing was held/unheld, so the command must not report success")
			assert.Contains(t, err.Error(), "not-installed")
			assert.Contains(t, err.Error(), "active lockfile")
			assert.Empty(t, out.String(), "a failure is not primary output; nothing goes to stdout")
		})
	}
}
