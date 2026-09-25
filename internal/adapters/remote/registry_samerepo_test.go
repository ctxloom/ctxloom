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

// With no fallback, the registry could no longer tell a second registration
// of an unreadable URL from a new one, and each failing pull would register
// another remote. So the registry refuses the URL at the door instead.
func TestRegistry_RefusesAURLThatNamesNoRepository(t *testing.T) {
	reg := newResolveTestRegistry(t)
	const bad = "https://h/o/a%2Fb/r"

	err := reg.Add("bad", bad)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refuri.ErrSyntax), "%v", err)

	_, err = reg.GetOrCreateByURL(bad)
	require.Error(t, err)
	assert.True(t, errors.Is(err, refuri.ErrSyntax), "%v", err)

	bad2 := bad
	_, err = reg.Update("personal", RemoteEdit{URL: &bad2})
	require.Error(t, err)
	assert.True(t, errors.Is(err, refuri.ErrSyntax), "%v", err)

	assert.Len(t, reg.List(), 2, "nothing was registered")
}
