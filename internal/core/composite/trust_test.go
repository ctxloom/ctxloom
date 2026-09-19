package composite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// Trust is the gate holder. Tonight it holds today's gate value — the
// bundles.Authorizer every exposure and executable resolver consults — and
// the tests pin the two properties the holder must never lose: the wrapped
// decision is delivered unchanged, and the zero value admits nothing.

func TestFromAuthorizer_DeliversTheWrappedDecisionUnchanged(t *testing.T) {
	var seen bundles.Exposure
	gate := bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		seen = e
		return bundles.Verdict{Allow: false, Reason: bundles.ReasonRejected, Detail: "declined"}
	})
	tr := FromAuthorizer(gate)
	got := tr.Authorizer().Admit(bundles.Exposure{RefStr: "x#fragments/y"})
	assert.Equal(t, "x#fragments/y", seen.RefStr, "the exposure reaches the wrapped gate as given")
	assert.Equal(t, bundles.Verdict{Allow: false, Reason: bundles.ReasonRejected, Detail: "declined"}, got)
	assert.True(t, tr.Gates())
}

func TestTrust_ZeroValue_HasNoPermissiveAnswer(t *testing.T) {
	var zero Trust
	require.Nil(t, zero.Authorizer(), "a zero Trust holds no gate: the nil authorizer is the spelling bundles.Decide withholds on loudly")
	assert.True(t, zero.Gates(), "a zero Trust is not an ungated surface; it is a surface that forgot its gate")
}

func TestUngated_IsTheOnlySpellingOfAdmitEverything(t *testing.T) {
	tr := Ungated()
	assert.False(t, tr.Gates())
	v := tr.Authorizer().Admit(bundles.Exposure{})
	assert.True(t, v.Allow)
	assert.Equal(t, bundles.ReasonUngated, v.Reason)
}
