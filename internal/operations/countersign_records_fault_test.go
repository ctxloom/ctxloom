package operations

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// unresolvableUserStore puts homeApprovalsDir into its refusal branch — the
// stand-in for the production doors to the same fault (os.UserHomeDir failing
// under a systemd unit, `env -i`, a container with no HOME). It returns the
// substring the resolver's own error carries, so the assertions below pin
// that THAT error is the one that reaches the caller.
func unresolvableUserStore(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", string(filepath.Separator)+"ctxloom-unsandboxed-home")
	_, err := homeApprovalsDir()
	require.Error(t, err, "fixture precondition: the user store must be unresolvable here")
	return "SetHomeApprovalsDirForTesting"
}

// TestBuildCountersignRecords_UnresolvableUserStore_KeepsTheCause pins the
// UNCONFIGURED state as a fault that carries its own cause.
//
// buildCountersignRecords used to turn homeApprovalsDir's error into
// `userDir = ""` and continue. The resulting store is inert, so the session
// did fail closed — but the ERROR was gone. All a user got back was
// countersign.Store.configured's second-hand guess, "no directory configured
// (unresolvable home directory?)", with the question mark doing the work the
// original error could have done outright. A fault reported as a value loses
// the one thing that makes it fixable.
func TestBuildCountersignRecords_UnresolvableUserStore_KeepsTheCause(t *testing.T) {
	want := unresolvableUserStore(t)

	records := buildCountersignRecords(nil, afero.NewOsFs(), nil, nil, nil)

	err := records.readable()
	require.Error(t, err, "an unconfigured user store must never resolve as readable")
	assert.Contains(t, err.Error(), want,
		"the resolver's own error must survive to the caller, not be replaced by a guess")
	assert.Contains(t, err.Error(), "user approvals store")
}

// TestEffectiveTrust_UnresolvableUserStore_DeniesAndNamesTheCause is the same
// fault seen from the decision function: the deny is the part that already
// worked, the named cause is the part that did not. Both are asserted here so
// a future change that restores the fail-closed posture while dropping the
// diagnosis still goes red.
func TestEffectiveTrust_UnresolvableUserStore_DeniesAndNamesTheCause(t *testing.T) {
	resetStrictness(t)
	want := unresolvableUserStore(t)

	ref := trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"}
	mark := strictness.Checkpoint()
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:        ref,
		Posture:    postureCtxOf(ref),
		Provenance: postureProvOf(ref),
		Payload:    pbytes("x"),
		Form:       rawForm,
		FS:         afero.NewMemMapFs(),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)

	found := strictness.Since(mark)
	require.NotEmpty(t, found, "an unconfigured approvals store is a trust-class fault, not a quiet deny")
	var trustMsgs string
	for _, f := range found {
		if f.Class == strictness.ClassTrust {
			trustMsgs += f.Message
		}
	}
	assert.Contains(t, trustMsgs, want, "the finding must name why the store could not be resolved")
}
