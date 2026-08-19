package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestResolveItemAsk_CompanionRefsFailClosed closes a fail-open gap: the
// operations-side "does this look like a source ref?" predicate used to list
// three markers ("://", git@, ctxloom:local@) while remote.ParseReference
// dispatched on six — the missing one being ctxloom:companion@. A companion
// ref that FAILED validation therefore fell through to the bare-name branch
// and came back IsLocal, i.e. auto-trusted at step 3, while a WELL-FORMED
// companion ref went through the normal cascade and sat pending. Malformed
// input was trusted more than valid input.
//
// The fix is structural rather than a fourth marker: every marker list is
// remote.IsSelfContainedRef, and the ask boundary refuses that whole class
// rather than guessing at it.
func TestResolveItemAsk_CompanionRefsFailClosed(t *testing.T) {
	var empty bundles.Catalog

	for _, base := range []string{
		"ctxloom:companion@",        // empty bin name
		"ctxloom:companion@../evil", // traversal in the bin name
		"ctxloom:companion@/abs",    // absolute bin path
		"ctxloom:companion@ltk",     // even well-formed: this spelling is retired input
	} {
		_, err := ResolveItemAsk(empty, base+"#fragments/x")
		assert.Error(t, err, "%q must fail closed, never resolve as a local bundle name", base)
	}

	// The CANONICAL companion spelling resolves, and is not local: a companion
	// is its OWN exemption step, never laundered through the first-party one.
	br, err := ResolveItemAsk(empty, "ctxloom+companion:ltk#fragments/x")
	require.NoError(t, err)
	assert.Equal(t, trust.ClassCompanion, br.Class)
	tRef := trust.RefFromBundleRef(br)
	assert.True(t, tRef.IsCompanion)
	assert.False(t, tRef.IsLocal, "a valid companion ref is not a first-party local bundle")
}

// TestIsSelfContainedRef_OneList pins the union the two former predicates now
// share. It bites in both directions:
//
//   - the operations copy's Contains("://") reach (any scheme, not just the
//     four ParseReference dispatches on) must survive, or an ssh:// ref that
//     fails to parse would silently downgrade to a local bundle name;
//   - the remote copy's ctxloom:companion@ marker must survive, or
//     the same fail-open gap reopens.
func TestIsSelfContainedRef_OneList(t *testing.T) {
	selfContained := []string{
		"https://github.com/acme/repo",
		"http://github.com/acme/repo",
		"file:///srv/repo",
		"ssh://git@host/acme/repo",
		"git://host/acme/repo",
		"git@github.com:acme/repo",
		"ctxloom:local@bundles/x",
		"ctxloom:companion@ltk",
		"ctxloom:companion@",
	}
	for _, ref := range selfContained {
		assert.True(t, remote.IsSelfContainedRef(ref), "%q carries its own scheme/source token", ref)
	}

	bare := []string{"my-tools", "lang/go", "demo@v1", "", "ctxloom:local", "ctxloom:companion"}
	for _, ref := range bare {
		assert.False(t, remote.IsSelfContainedRef(ref), "%q carries no scheme/source token", ref)
	}
}
