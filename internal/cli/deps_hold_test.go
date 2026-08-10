package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// `deps hold` / `deps unhold` flip a flag on the ACTIVE LOCKFILE entry, so
// when the named item has no such entry nothing is flipped and nothing is
// persisted. That whole branch used to read as the project's signature silent
// no-op and was made a hard error. It is really two situations the lockfile
// cannot tell apart, and only one of them is a failure — see
// reportNothingToHold. These two tests pin both arms so neither can drift back
// into the other.

// A name that resolves to NOTHING — a typo, the wrong project, a `deps pull`
// that never ran — is the real failure case: the caller asked to freeze a
// dependency, nothing was frozen, and exit 0 would tell them it was. No
// acceptance scenario ever covered this arm.
func TestBundleHoldUnhold_UnknownItemFailsInsteadOfNoOpping(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*cobra.Command, []string) error
	}{
		{"hold", depsHoldCmd.RunE},
		{"unhold", depsUnholdCmd.RunE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupEditProject(t) // a project with no lockfile and no bundles at all

			var out, errOut bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			err := tc.run(cmd, []string{"not-installed"})

			require.Error(t, err, "nothing was held/unheld, so the command must not report success")
			assert.Contains(t, err.Error(), "not-installed")
			assert.Contains(t, err.Error(), "active lockfile")
			assert.Empty(t, out.String(), "a failure is not primary output; nothing goes to stdout")
		})
	}
}

// A LOCAL bundle is the arm the acceptance suite has specified since the
// command was `bundle pin` ("Holding a local bundle reports it is not
// lockfile-tracked", which asserts BOTH hold and unhold succeed). It has no
// upstream and no lockfile entry, so `deps upgrade` can never advance it: the
// guarantee hold exists to give already holds, and unhold has nothing to
// release. That is a benign no-op, not a user error — exit 0.
//
// The notice is still about work NOT done, so it must ride STDERR; stdout
// stays empty because nothing was pinned.
func TestBundleHoldUnhold_LocalBundleSucceedsWithANoticeOnStderr(t *testing.T) {
	for _, verb := range []string{"hold", "unhold"} {
		t.Run(verb, func(t *testing.T) {
			run := depsHoldCmd.RunE
			if verb == "unhold" {
				run = depsUnholdCmd.RunE
			}
			cfg := setupEditProject(t)
			_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
			require.NoError(t, err)

			var out, errOut bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)

			require.NoError(t, run(cmd, []string{"demo"}),
				"a local bundle is already unadvanceable by `deps upgrade`; asking to %s it is not a user error", verb)
			assert.Contains(t, errOut.String(), "nothing to "+verb,
				"the acceptance suite asserts this wording (bundle.feature)")
			assert.Contains(t, errOut.String(), "local bundle")
			assert.Empty(t, out.String(), "nothing was pinned, so nothing belongs on stdout")
		})
	}
}
