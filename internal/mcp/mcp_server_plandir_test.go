package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSessionInstructions_PlanDirIsTheDurableOne pins the sentence that CREATES
// the population of undurable plan files. Every session is told right here
// where to put its plans, so if this sentence names the harp top level, every
// containerized agent that obeys it writes a design note into overlay space
// and loses it at teardown — a successful write and zero surviving bytes.
//
// The assertion is on the DIRECTORY the instruction hands out, not on prose:
// it must be paths.HarpPlansDir, and the harp top level must not appear as a
// place to write. Both halves are needed. Naming persist/ while still also
// offering the top level would leave the agent free to pick the one that
// vanishes, and an assertion that only checked for the substring "persist"
// would pass on a sentence that mentioned it in passing.
func TestSessionInstructions_PlanDirIsTheDurableOne(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"

	planDir, err := paths.HarpPlansDir(harp)
	require.NoError(t, err)
	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NotEqual(t, harpDir, planDir, "the plan dir must not be the harp top level")

	got := sessionInstructions(harp)

	assert.Contains(t, got, "`"+planDir+"`",
		"the instruction must name the harp's persist dir — the only part of the harp dir a container writes through to the host")
	assert.Contains(t, got, "`"+filepath.Join(planDir, "v1-removal"+paths.PlanFileExt)+"`",
		"the worked example must sit in the same durable directory, not demonstrate the undurable one")
	assert.NotContains(t, got, "`"+harpDir+"`",
		"the harp top level must not be offered as a place to write: a plan left there dies with the container")
	assert.Equal(t, 0, strings.Count(got, "`"+harpDir+string(filepath.Separator)+"v1-removal"+paths.PlanFileExt+"`"),
		"and neither must the example")
}

// TestSessionInstructions_NoHarpAddsNoPlanDir: a caller with no session
// identity gets the bare server instructions. There is no harp to resolve a
// plan directory from, and inventing one would point an agent at a path no
// session owns.
func TestSessionInstructions_NoHarpAddsNoPlanDir(t *testing.T) {
	testsupport.Isolate(t)
	got := sessionInstructions("")
	assert.Equal(t, mcpServerInstructions, got)
	assert.NotContains(t, got, paths.PlanFileExt)
}
