package remote

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/refuri"
)

// A string that names no repository is the same repository as nothing — not
// even itself. A byte-equality fallback is a verbatim identity minted for a
// string nobody can address.
func TestSameRepository_AnUnreadableURLMatchesNothing(t *testing.T) {
	for _, u := range []string{"https://github.com", "https://h/o/a%2Fb/r", "not a url@x"} {
		assert.False(t, SameRepository(u, u), "%q matched itself", u)
		assert.False(t, SameRepository(u, "https://github.com/o/r"), u)
	}
	assert.True(t, SameRepository("git@github.com:o/r", "https://GitHub.com/o/r/"))
}

// A URL that names no repository has no identity to register or look up, so
// the registry refuses it at the door and no lookup ever finds it.
func TestRegistry_RefusesAURLThatNamesNoRepository(t *testing.T) {
	reg := newResolveTestRegistry(t)
	const bad = "https://h/o/a%2Fb/r"

	err := reg.Add("bad", bad)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refuri.ErrSyntax), "%v", err)

	_, found := reg.LookupURL(bad)
	assert.False(t, found, "an address that names no repository finds no remote")

	bad2 := bad
	_, err = reg.Update("personal", RemoteEdit{URL: &bad2})
	require.Error(t, err)
	assert.True(t, errors.Is(err, refuri.ErrSyntax), "%v", err)

	assert.Len(t, reg.List(), 2, "nothing was registered")
}
