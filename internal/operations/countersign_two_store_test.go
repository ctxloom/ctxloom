package operations

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestCountersignRecords_AbsentUserStore_ProjectStoreStillDecides pins the
// SUPPORTED CONFIGURATION a strict reading of amused-fondue's four states
// would have broken: a container or a CI runner.
//
// The two stores are not two copies of one thing. The USER store
// (~/.ctxloom/approvals) is personal, global to the machine, and NEVER
// committed; the PROJECT store (<repo>/.ctxloom/approvals) is committable and
// is how a team or a CI run inherits a decision. A fresh container or CI
// runner therefore has NO user store BY DESIGN and gets its trust from the
// project store alone (task finicky-estrogen).
//
// So an absent USER store beside a readable PROJECT store must keep deciding
// from the project store and must not fail the run. This test asserts the
// asymmetry directly: the rejection lives only in the project store, the user
// store does not exist at all, and the verdict must still be the rejection.
func TestCountersignRecords_AbsentUserStore_ProjectStoreStillDecides(t *testing.T) {
	resetStrictness(t)
	f := newTrustFixture(t)

	// The user store's directory is never created — the container shape. The
	// rejection is SIGNED and lands in the PROJECT store, which is the only
	// place a shareable decision may live (an unsigned record there would be a
	// forgery primitive — spec §9.5).
	userDir := filepath.Join(t.TempDir(), "never-created", "approvals")
	user := countersign.NewStore(userDir, f.fs())
	userState, _ := user.Resolve()
	require.Equal(t, countersign.StateAbsent, userState, "fixture precondition")

	ref := trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"}
	require.NoError(t, f.project.WriteRefReject(mustCountersignRef(t, ref), f.signer))
	projectState, perr := f.project.Resolve()
	require.Equal(t, countersign.StateReadable, projectState)
	require.NoError(t, perr)

	records := countersignRecords{user: user, project: f.project, root: f.root}

	require.NoError(t, records.readable(),
		"an absent user store beside a readable project store is a SUPPORTED configuration (containers, CI) and must not fail the run")

	mark := strictness.Checkpoint()
	assert.True(t, records.Rejected(ref, pbytes("x")),
		"the project store's recorded rejection must still decide when the user store does not exist")
	assert.Empty(t, strictness.Since(mark),
		"deciding from the project store alone is not a fault and must record no finding")
}

// TestCountersignRecords_UnreadableProjectStore_FailsEvenWithAReadableUser is
// the other half of the same asymmetry, and the reason the tolerance above is
// scoped to ABSENT rather than to "the user store": a store that EXISTS and
// cannot be read might be hiding a rejection, and that is a fault in either
// store regardless of how healthy the other one looks. Without this, a change
// that relaxed the pair to "either store readable is enough" would pass the
// test above.
func TestCountersignRecords_UnreadableProjectStore_FailsEvenWithAReadableUser(t *testing.T) {
	fs := afero.NewMemMapFs()
	userDir := "/user/approvals"
	require.NoError(t, fs.MkdirAll(userDir, 0o755))
	projectDir := "/project/.ctxloom/approvals"
	require.NoError(t, fs.MkdirAll(projectDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(projectDir, "0bad.sig"), []byte("not armored\n"), 0o644))

	records := countersignRecords{
		user:    countersign.NewStore(userDir, fs),
		project: countersign.NewStore(projectDir, fs),
	}
	err := records.readable()
	require.Error(t, err, "an UNREADABLE store is a fault in either position; only ABSENT is tolerated")
	assert.Contains(t, err.Error(), "project approvals store")
}
