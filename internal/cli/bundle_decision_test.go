package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The content-decision verbs, as a SET.
//
// The store holds three states — approved, rejected, undecided — so the CLI
// carries three verbs, one per state, and every one of them must be reachable
// by the name that says what it writes. `untrust` was the second verb's old
// spelling and it lied: it reads as the inverse of `trust` while writing a
// rejection, which is stronger and stickier than withdrawing an approval, so
// someone reaching for it to undo a `trust` did something they did not mean.
//
// This asserts the SURFACE rather than any one command's behaviour, because
// the surface is the part a rename can quietly half-finish: a `reject` that
// exists alongside a surviving `untrust` leaves the trap in place for everyone
// who already knows the old spelling.

// bundleSubcommand returns the named direct subcommand of `ctxloom bundle`, or
// nil when the noun does not carry it. Every name a command answers to is
// checked (cobra's Name + aliases), so an alias cannot smuggle a retired verb
// back onto the surface.
func bundleSubcommand(name string) *cobra.Command {
	for _, c := range bundleCmd.Commands() {
		if c.Name() == name {
			return c
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return c
			}
		}
	}
	return nil
}

// TestBundleCarriesOneVerbPerDecisionState pins the three-verb surface: one way
// into each state the store can hold, and no path back to the misleading
// spelling.
func TestBundleCarriesOneVerbPerDecisionState(t *testing.T) {
	for name, writes := range map[string]string{
		"trust":  "an approval — the item is delivered",
		"reject": "a rejection — the item is withheld everywhere",
		"forget": "nothing; it CLEARS whichever decision is recorded",
	} {
		cmd := bundleSubcommand(name)
		require.NotNilf(t, cmd, "`ctxloom bundle %s` must exist: it is the only way to record %s", name, writes)
		assert.NotEmptyf(t, cmd.Short, "`ctxloom bundle %s` must describe itself", name)
	}

	assert.Nil(t, bundleSubcommand("untrust"),
		"`ctxloom bundle untrust` must be gone, not aliased: it reads as the inverse of `trust` and is not one — "+
			"it writes a rejection, which overrides a trusted publisher and overrides local content. "+
			"Leaving it reachable leaves the trap for everyone who already knows the old spelling")
}

// localFragmentState resolves what a listing shows for a project-authored
// fragment — the same TrustStamper/EffectiveTrust path `ctxloom fragment list`
// stamps, so the assertion reads the EFFECT a user sees and not the mutation's
// own report of itself.
func localFragmentState(t *testing.T, cfg *config.Config, ref string) string {
	t.Helper()
	rows, err := listItemRows(cfg, ItemTypeFragment)
	require.NoError(t, err)
	stampItemTrust(cfg, ItemTypeFragment, rows)
	row, ok := findRow(rows, ref)
	require.Truef(t, ok, "%s must be listed", ref)
	return row.State
}

// TestRunItemForget_ClearsARejection is the case the third verb exists for.
//
// A rejection beats every exemption, including the first-party allowance a
// project's own content enjoys, so the flip is visible from BOTH sides in one
// fixture: the fragment is delivered, then rejected and withheld, then cleared
// and delivered again. The middle state is what proves the last assertion could
// have failed.
func TestRunItemForget_ClearsARejection(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "curl-pipe-sh", "rm -rf danger")
	const ref = "demo#fragments/curl-pipe-sh"

	require.Equal(t, "accepted", localFragmentState(t, cfg, ref), "the project's own content is delivered before anyone decides anything")

	c, _ := testCmd()
	require.NoError(t, runItemReject(c, cfg, ref))
	require.Equal(t, "rejected", localFragmentState(t, cfg, ref), "the rejection must beat the local allowance, or there is nothing to clear")

	c, out := testCmd()
	require.NoError(t, runItemForget(c, cfg, ref))
	assert.Contains(t, out.String(), "Forgot demo#fragments/curl-pipe-sh")
	assert.Contains(t, out.String(), "rejection")

	assert.Equal(t, "accepted", localFragmentState(t, cfg, ref),
		"clearing the rejection returns the item to undecided, so the first-party allowance applies again")

	store := userApprovalsStore(t)
	itemRef := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "curl-pipe-sh", IsLocal: true}
	assert.False(t, store.HasUnsignedRefReject(countersignRefFor(t, itemRef)), "the sticky ref block must be gone from the store")
	assert.False(t, store.HasUnsignedContentReject(signing.AttestFragmentRaw, []byte("rm -rf danger")),
		"the content block must go too: a rejection cleared by half still rejects the same bytes under any other name")
}

// TestRunItemForget_ClearsAnApproval: the other decided state, read off the
// record. A project-authored fragment is delivered either way (the first-party
// exemption decides it before any approval is consulted), so the acceptance's
// one observable consequence is the countersignature — and that is what this
// asserts on, in both directions.
func TestRunItemForget_ClearsAnApproval(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "approved body")

	c, _ := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "demo#fragments/x"))

	store := userApprovalsStore(t)
	itemRef := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "x", IsLocal: true}
	require.True(t, store.HasUnsignedApprove(countersignRefFor(t, itemRef), signing.AttestFragmentRaw, []byte("approved body")),
		"the approval must be on file before it can meaningfully be cleared")

	c, out := testCmd()
	require.NoError(t, runItemForget(c, cfg, "demo#fragments/x"))
	assert.Contains(t, out.String(), "approval")
	assert.False(t, store.HasUnsignedApprove(countersignRefFor(t, itemRef), signing.AttestFragmentRaw, []byte("approved body")),
		"the approval must be gone from the store, not merely reported as gone")
}

// TestRunItemForget_NothingRecorded_SaysSo. An undecided item has nothing to
// clear, and printing "Forgot …" over it would teach a user that a mistyped ref
// worked.
func TestRunItemForget_NothingRecorded_SaysSo(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "x", "never decided")

	c, out := testCmd()
	require.NoError(t, runItemForget(c, cfg, "demo#fragments/x"))
	assert.Contains(t, out.String(), "No decision was recorded")
	assert.NotContains(t, out.String(), "Forgot")
}
