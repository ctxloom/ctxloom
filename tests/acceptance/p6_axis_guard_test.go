// Package acceptance: the P6 axis guard's own check.
//
// UNTAGGED, matching the subject it checks: p6BuildableCells and
// p6FixtureBuilds live in the untagged probe_p6_steer_echo.go, so this needs no
// build tag to compile, and `go vet -tags integration ./tests/...` (the lint
// gate) type-checks it on every run.
//
// MEASURED, because the obvious stronger claim is false and was nearly written
// here: this does NOT execute under `just test-integration`, and `just test-pkg
// ./tests/acceptance` REFUSES an untagged run of this package outright ("every
// godog scenario is excluded, so its green would mean nothing"). So the tag-free
// form buys compilation reach, not execution reach — in practice this runs on
// the acceptance-tagged run alongside the fixture check it complements.
package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestP6Fixture_DeclaresOnlyAxesItBuilds pins the guard's discrimination: every
// declared pair must be buildable, and pairs the fixture does NOT construct must
// be refused. Without the negative half a guard stuck at `true` would pass.
func TestP6Fixture_DeclaresOnlyAxesItBuilds(t *testing.T) {
	// THE FOUR CORNERS MUST BE DECLARED, asserted POSITIVELY and by name.
	// Without this the test is satisfied by whatever the slice happens to hold:
	// it iterates the declarations and checks each is buildable, so DELETING a
	// corner simply shortens the loop and stays green. Measured — dropping
	// {container-rootless, none} survived mutation until this assertion existed.
	//
	// The matrix is the product of the two independent axes, and a corner that
	// silently disappears takes its live cell with it: the Examples row would
	// then be refused by the axis guard, which reads as "that cell was never
	// meant to run" rather than "somebody deleted it".
	for _, want := range [][2]string{
		{"host", "none"},
		{"host", "worktree"},
		{"container-rootless", "none"},
		{"container-rootless", "worktree"},
	} {
		assert.True(t, p6FixtureBuilds(want[0], want[1]),
			"matrix corner runtime=%q workspace=%q must be declared buildable; if it was removed on purpose, remove its Examples row too", want[0], want[1])
	}
	assert.Len(t, p6BuildableCells, 4,
		"the buildable set is exactly the four claude matrix corners — a fifth entry means a cell nothing else in this test knows about")

	require.NotEmpty(t, p6BuildableCells)
	for _, c := range p6BuildableCells {
		assert.True(t, p6FixtureBuilds(c.Runtime, c.Workspace),
			"declared cell %s/%s must be buildable", c.Runtime, c.Workspace)
		assert.Contains(t, p6BuildableAxes(), c.Runtime,
			"the refusal message must name every buildable pair, or it sends the reader nowhere")
	}
	// Pairs the fixture does not construct. container-rootful is here on
	// purpose: it is a real axis value that no box this suite runs on can
	// reach, so it must be refused rather than silently treated as rootless.
	// container-rootful is the important one: it is a REAL axis value that no
	// box this suite runs on can reach, so it must be refused rather than
	// silently treated as rootless — the ownership substitution the isolation
	// doctrine forbids outright. "container" is the retired undifferentiated
	// spelling, which must not resolve either.
	for _, c := range [][2]string{
		{"container-rootful", "worktree"},
		{"container-rootful", "none"},
		{"container", "worktree"},
		{"host", "shared"},
		{"", ""},
	} {
		assert.False(t, p6FixtureBuilds(c[0], c[1]),
			"runtime=%q workspace=%q is not built by this fixture and must be refused, not relabelled", c[0], c[1])
	}
}
