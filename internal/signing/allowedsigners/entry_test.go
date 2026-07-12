package allowedsigners

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- usage-demonstrating ---

func TestEntry_MatchesPrincipal(t *testing.T) {
	e := Entry{Principals: []string{"ben@abbitt.me", "lead@team.example"}}
	assert.True(t, e.MatchesPrincipal("ben@abbitt.me"))
	assert.True(t, e.MatchesPrincipal("lead@team.example"))
	assert.False(t, e.MatchesPrincipal("stranger@example.com"))
}

func TestEntry_MatchesNamespace_RestrictedToListedNamespaces(t *testing.T) {
	e := Entry{Namespaces: []string{"publish.v1.ctxloom.dev"}}
	assert.True(t, e.MatchesNamespace("publish.v1.ctxloom.dev"))
	assert.False(t, e.MatchesNamespace("approve.v1.ctxloom.dev"))
}

func TestEntry_MatchesNamespace_AbsentOptionAcceptsAll(t *testing.T) {
	// Verified against real ssh-keygen: an entry with no namespaces=
	// option verifies successfully under any namespace.
	e := Entry{Namespaces: nil}
	assert.True(t, e.MatchesNamespace("publish.v1.ctxloom.dev"))
	assert.True(t, e.MatchesNamespace("approve.v1.ctxloom.dev"))
	assert.True(t, e.MatchesNamespace("anything-at-all"))
}

func TestEntry_ValidAt_WithinBounds(t *testing.T) {
	after := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2021, 12, 31, 0, 0, 0, 0, time.UTC)
	e := Entry{ValidAfter: &after, ValidBefore: &before}

	assert.True(t, e.ValidAt(time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)))
}

// --- edge cases ---

func TestEntry_MatchesNamespace_PresentButEmptyMatchesNothing(t *testing.T) {
	// Verified against real ssh-keygen: namespaces="" parses without
	// error and then matches no namespace (an inert, not unrestricted,
	// entry). Distinct from Namespaces == nil.
	e := Entry{Namespaces: []string{}}
	assert.False(t, e.MatchesNamespace("publish.v1.ctxloom.dev"))
	assert.False(t, e.MatchesNamespace(""))
}

func TestEntry_ValidAt_NoBoundsAlwaysValid(t *testing.T) {
	e := Entry{}
	assert.True(t, e.ValidAt(time.Now()))
	assert.True(t, e.ValidAt(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)))
}

func TestEntry_ValidAt_BoundaryIsInclusive(t *testing.T) {
	// Verified against real ssh-keygen error text ("verify time X > valid-before Y"
	// / "verify time X < valid-after Y"): the comparison is strict, so the
	// exact boundary instant is itself valid.
	after := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2021, 12, 31, 0, 0, 0, 0, time.UTC)
	e := Entry{ValidAfter: &after, ValidBefore: &before}

	assert.True(t, e.ValidAt(after), "exactly valid-after should be valid")
	assert.True(t, e.ValidAt(before), "exactly valid-before should be valid")
}

func TestEntry_ValidAt_BeforeAfterBound(t *testing.T) {
	after := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	e := Entry{ValidAfter: &after}
	assert.False(t, e.ValidAt(after.Add(-time.Second)))
	assert.True(t, e.ValidAt(after.Add(time.Second)))
}

func TestEntry_ValidAt_AfterBeforeBound(t *testing.T) {
	before := time.Date(2021, 12, 31, 0, 0, 0, 0, time.UTC)
	e := Entry{ValidBefore: &before}
	assert.True(t, e.ValidAt(before.Add(-time.Second)))
	assert.False(t, e.ValidAt(before.Add(time.Second)))
}

func TestEntry_MatchesPrincipal_GlobPattern(t *testing.T) {
	e := Entry{Principals: []string{"*@example.com"}}
	assert.True(t, e.MatchesPrincipal("anyone@example.com"))
	assert.False(t, e.MatchesPrincipal("anyone@example.org"))
}
