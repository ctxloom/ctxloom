package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBareNoun_AnswersWithItsDefaultView drives EVERY namespace wired to a
// default child and asserts the bare form actually produces that child's
// answer.
//
// The walk is derived from the tree rather than from a list of nouns, so a
// namespace wired later is covered without editing this test — and one that is
// unwired by accident simply stops being checked, which is why
// TestGroupNodeDefault_WiredCountHoldsSteady guards the count.
//
// Byte equality against the explicit spelling is the assertion that bites.
// Asserting only "no help marker" passes against a command that prints
// NOTHING, and asserting only "non-empty" passes against help. Both of those
// failures have shipped in this project before, so both are ruled out here.
//
// The walk is the HUMAN half of the ladder throughout — a bare noun is what
// somebody types while exploring — so it presents a terminal. One noun cares:
// `ctxloom mcp` refuses a caller that is not a person, because that caller is
// most likely an engine expecting JSON-RPC (mcpBareMachineRefusal).
func TestBareNoun_AnswersWithItsDefaultView(t *testing.T) {
	for _, tc := range wiredBareNouns(t) {
		t.Run(strings.ReplaceAll(tc.path, " ", "_"), func(t *testing.T) {
			remoteBareFixture(t)
			presentTerminal(t, true)

			bare, err := runRoot(t, tc.args...)
			require.NoError(t, err, "bare `%s` runs", tc.path)
			explicit, err := runRoot(t, append(tc.args, tc.child)...)
			require.NoError(t, err, "`%s %s` runs", tc.path, tc.child)

			assert.Equal(t, explicit, bare,
				"bare `%s` is the same entry point as `%s %s`", tc.path, tc.path, tc.child)
			assert.NotContains(t, bare, usageMarker,
				"bare `%s` answers; help has its own spelling", tc.path)
			assert.NotEmpty(t, strings.TrimSpace(bare),
				"bare `%s` answers with something — an empty answer is the silent no-op", tc.path)
		})
	}
}

// TestGroupNodeDefault_WiredCountHoldsSteady pins how many namespaces answer
// their bare form.
//
// Without it, unwiring a noun makes the walk above cover one fewer case and
// stay green — coverage that quietly shrinks reports success for work that
// stopped happening. Raise this number when wiring a noun; a drop is a
// regression to explain, not a number to edit.
func TestGroupNodeDefault_WiredCountHoldsSteady(t *testing.T) {
	const wiredToday = 17

	assert.Len(t, wiredBareNouns(t), wiredToday,
		"namespaces answering their bare form; raise when wiring one, investigate a drop")
}

// TestGroupNodeDefault_RunsTheChildsPreRunE pins that delegation performs the
// child's setup, not just its RunE.
//
// None of the nouns wired today has a PreRunE that changes an observable
// answer, so dropping the PreRunE call from the delegation path breaks nothing
// the walk above can see — measured, by removing it and watching the suite stay
// green. The first noun wired to a child that loads config or validates input
// in PreRunE would then silently skip that setup, which is the kind of omission
// that surfaces as a wrong answer rather than an error.
//
// A synthetic namespace is used rather than a real one precisely so the claim
// holds regardless of which nouns happen to be wired.
func TestGroupNodeDefault_RunsTheChildsPreRunE(t *testing.T) {
	var ranPreRunE, ranRunE bool

	child := &cobra.Command{
		Use:     "list",
		PreRunE: func(*cobra.Command, []string) error { ranPreRunE = true; return nil },
		RunE:    func(*cobra.Command, []string) error { ranRunE = true; return nil },
	}
	parent := groupNodeDefault(&cobra.Command{Use: "synthetic"}, "list")
	parent.AddCommand(child)

	require.NoError(t, runGroupNodeDefault(parent, "list"))

	assert.True(t, ranPreRunE, "delegation runs the child's PreRunE before its RunE")
	assert.True(t, ranRunE, "delegation runs the child's RunE")
}

// TestGroupNodeDefault_MissingChildFailsLoud covers the other edge: a delegate
// named for a child that is not there must say so rather than exiting 0 having
// done nothing.
func TestGroupNodeDefault_MissingChildFailsLoud(t *testing.T) {
	parent := groupNodeDefault(&cobra.Command{Use: "synthetic"}, "absent")

	err := runGroupNodeDefault(parent, "absent")

	require.Error(t, err, "a bare form with no delegate to run is an error, not a silent success")
	assert.Contains(t, err.Error(), "absent", "the error names the subcommand it looked for")
}

type bareNoun struct {
	path  string   // "mcp server", for messages
	args  []string // {"mcp", "server"}, the bare invocation
	child string   // the default child it delegates to
}

func wiredBareNouns(t *testing.T) []bareNoun {
	t.Helper()

	var found []bareNoun
	walkCommands(rootCommand(), func(c *cobra.Command) {
		child, ok := groupNodeDefaultChild(c)
		if !ok {
			return
		}
		path := c.CommandPath()
		found = append(found, bareNoun{
			path:  path,
			args:  strings.Fields(strings.TrimPrefix(path, "ctxloom ")),
			child: child,
		})
	})
	return found
}
