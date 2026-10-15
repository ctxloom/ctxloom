package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseBranch_BothSpellingsAreOneBranch is the property the type exists
// for: git's two spellings of one branch parse to EQUAL values, and each
// rendering is available explicitly.
func TestParseBranch_BothSpellingsAreOneBranch(t *testing.T) {
	short, err := ParseBranch("ctxloom/hoary-quick-elm")
	require.NoError(t, err)
	full, err := ParseBranch("refs/heads/ctxloom/hoary-quick-elm")
	require.NoError(t, err)

	assert.Equal(t, short, full, "refs/heads/x and x are the same branch")
	assert.True(t, short == full, "and compare equal with ==")
	assert.Equal(t, "ctxloom/hoary-quick-elm", full.Short())
	assert.Equal(t, "ctxloom/hoary-quick-elm", full.String())
	assert.Equal(t, "refs/heads/ctxloom/hoary-quick-elm", short.Ref())
	assert.False(t, short.IsZero())
}

// TestParseBranch_RefusesWhatIsNotABranch: only one refs/heads/ is stripped
// (a branch may itself be named "refs/heads/x" only by going through
// another namespace, which is refused), and the detached sentinel, the empty
// name, another ref namespace and whitespace are errors, not branches.
func TestParseBranch_RefusesWhatIsNotABranch(t *testing.T) {
	for _, s := range []string{"", "refs/heads/", "HEAD", "refs/heads/HEAD", "refs/tags/v1", "refs/heads/refs/tags/v1", "has space", "tab\there", "nl\n", " lead", "\tlead", "bell\x07"} {
		_, err := ParseBranch(s)
		assert.Error(t, err, "%q is not a branch", s)
	}
}

// TestBranch_ZeroIsNoBranch: the zero value renders empty in BOTH spellings —
// never a bare "refs/heads/" that git would read as a ref.
func TestBranch_ZeroIsNoBranch(t *testing.T) {
	var b Branch
	assert.True(t, b.IsZero())
	assert.Empty(t, b.Short())
	assert.Empty(t, b.Ref())
}

func TestMustParseBranch_PanicsOnABadName(t *testing.T) {
	assert.Equal(t, "main", MustParseBranch("refs/heads/main").Short())
	assert.Panics(t, func() { MustParseBranch("HEAD") })
}
