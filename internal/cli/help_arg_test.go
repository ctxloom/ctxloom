package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// helpArgBehaviour is what a `<verb> <name>` command does when the name it is
// given is literally "help". There are three answers in this package and the
// difference between them is the whole point of this test.
type helpArgBehaviour int

const (
	// helpBeforeConfig: the courtesy shortcut fires unconditionally, BEFORE
	// any config is loaded. This is the original pre-existence guard and it
	// keeps the property that made the shortcut worth having — `ctxloom
	// profile show help` renders help in a directory with no ctxloom config at
	// all, touching no filesystem state whatsoever.
	//
	// It is also the reading that makes a resource named "help" unaddressable,
	// which is why only the four profile commands still have it; the migration
	// off it is tracked (see helpShortcut's doc). A test pinning the no-config
	// property here is pinning something SLATED TO CHANGE — when profile.go
	// moves to the fallback below, move these rows too rather than treating
	// the red as a regression.
	helpBeforeConfig helpArgBehaviour = iota

	// helpAsFallback: the command loads config and looks the resource up
	// first, and renders help only when there is no such resource — where the
	// command was going to fail anyway, so the courtesy costs nothing. A
	// bundle or agent literally named "help" is honoured instead.
	//
	// These commands have LOST the no-config property in the strict sense:
	// they reach GetConfig() before they can know whether the shortcut
	// applies, and config resolution has a side effect (findAppDir creates
	// ~/.ctxloom when it falls back to the home layer). What the user actually
	// relied on survives — `ctxloom bundle show help` in a directory with no
	// ctxloom config still prints help and exits 0 — because a config-less
	// load succeeds with an empty config rather than erroring. That is what
	// the assertions below check: help still renders with no config present.
	helpAsFallback

	// actsOnResource: no shortcut at all. Naming the thing to create or write
	// is unambiguous, so `bundle create help` creates a bundle named "help".
	// Guarding these turned a genuine request into "print help, exit 0" —
	// success reported, nothing created, this project's signature bug.
	//
	// Pinned by ASSERTING THE RESOURCE EXISTS afterwards, not merely by
	// deleting the old expectation: a silent no-op satisfies "help was not
	// rendered" just as happily as a real create does.
	actsOnResource
)

// helpArgCommands is every `<verb> <name>` command in the package that takes a
// mandatory name argument, with the behaviour each one is pinned to. The list
// is exhaustive by construction — every site that mentions helpArgName or
// calls helpShortcut appears here, plus the two that deliberately mention
// neither (bundle create, agent create).
var helpArgCommands = []struct {
	path      []string
	behaviour helpArgBehaviour
	// exists reports whether the resource named "help" is now present.
	// Required for (and used only by) actsOnResource rows.
	exists func(t *testing.T) bool
}{
	{path: []string{"bundle", "create"}, behaviour: actsOnResource, exists: bundleHelpExists},
	{path: []string{"bundle", "edit"}, behaviour: helpAsFallback},
	{path: []string{"bundle", "show"}, behaviour: helpAsFallback},
	{path: []string{"agent", "show"}, behaviour: helpAsFallback},
	{path: []string{"agent", "create"}, behaviour: actsOnResource, exists: agentHelpExists},
	{path: []string{"agent", "default"}, behaviour: helpAsFallback},
	{path: []string{"agent", "remove"}, behaviour: helpAsFallback},
	{path: []string{"profile", "create"}, behaviour: helpBeforeConfig},
	{path: []string{"profile", "remove"}, behaviour: helpBeforeConfig},
	{path: []string{"profile", "show"}, behaviour: helpBeforeConfig},
	{path: []string{"profile", "modify"}, behaviour: helpBeforeConfig},
}

// TestHelpArgShortcut_BehaviourForEveryNameTakingCommand pins the arg-position
// "help" behaviour across all eleven sites at the PUBLIC SEAM (a real command
// invocation) rather than against any one copy of the idiom.
//
// It began life as a parity pin for a pure deduplication: eleven VERBATIM
// copies of one guard, collapsed onto helpShortcut, with this test green
// before and after to show the collapse was behaviour-preserving. The
// deduplication and the fix for the guard's underlying defect ("help" was an
// unaddressable resource name, and `bundle create help` reported success while
// creating nothing) landed independently, so the eleven are no longer uniform.
// Two things follow, and both are the test's job now:
//
//   - the seam is still the right place to assert, and every one of the eleven
//     is still listed — a command silently dropping out of this table is the
//     regression the list exists to catch;
//   - the assertion is per-behaviour, not shared. Deleting the rows that no
//     longer render help would have made the pin agree with the code by saying
//     less; pinning what they do INSTEAD is what makes a revert red.
//
// EACH SUBTEST GETS ITS OWN PROJECT ROOT. When they shared one, the table's own
// ordering became a hidden fixture: `bundle create help` created a real bundle
// that `bundle show help` then found, and `agent create help` defined the agent
// that `agent default help` then bound — later rows "failing" on state earlier
// rows had written.
func TestHelpArgShortcut_BehaviourForEveryNameTakingCommand(t *testing.T) {
	for _, tc := range helpArgCommands {
		name := tc.path[0] + " " + tc.path[1]
		t.Run(name, func(t *testing.T) {
			// A fresh project dir AND a fresh home per subtest: no config
			// anywhere (the shortcut must not need one) and no leakage from
			// the row before.
			testsupport.ProjectDir(t)
			config.Invalidate()
			t.Cleanup(config.Invalidate)
			home, err := os.UserHomeDir()
			require.NoError(t, err)

			// Find() rather than rootCmd.Execute(): Execute() lazily
			// materialises cobra's built-in `help` command onto the root,
			// which the --format coverage walk then reports as an
			// unregistered command. Find is a pure traversal.
			cmd, _, err := rootCmd.Find(tc.path)
			require.NoError(t, err)
			require.NotNil(t, cmd.RunE, "%s must have a RunE", name)

			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetContext(context.Background())
			t.Cleanup(func() {
				cmd.SetOut(nil)
				cmd.SetContext(context.Background())
			})

			require.NoError(t, cmd.RunE(cmd, []string{"help"}),
				"`ctxloom %s help` must succeed", name)

			switch tc.behaviour {
			case helpBeforeConfig, helpAsFallback:
				assert.Contains(t, out.String(), "Usage:",
					"`ctxloom %s help` must render help, not fail looking for an item named \"help\"", name)
				assert.Contains(t, out.String(), tc.path[1],
					"the help rendered must be THIS command's, not a parent's")
			case actsOnResource:
				assert.NotContains(t, out.String(), "Usage:",
					"`ctxloom %s help` names the resource to write; it must NOT print help instead", name)
				assert.True(t, tc.exists(t),
					"`ctxloom %s help` must actually create the resource — reporting success and writing nothing is the exact defect the old guard caused here", name)
			}

			// The no-config property, made observable. config.findAppDir
			// CREATES ~/.ctxloom when it falls back to the home layer, so an
			// app dir in the isolated home afterwards is proof the command
			// loaded config, and its absence is proof it did not. That is the
			// only difference between the first two behaviours, and it is why
			// they are separate constants rather than one "renders help".
			_, statErr := os.Stat(filepath.Join(home, ".ctxloom"))
			if tc.behaviour == helpBeforeConfig {
				assert.True(t, os.IsNotExist(statErr),
					"`ctxloom %s help` must render help WITHOUT loading config — that is what lets it work in a directory with no ctxloom config at all", name)
			} else {
				assert.NoError(t, statErr,
					"`ctxloom %s help` resolves config before it can know the shortcut applies; if that stopped being true, move this row to helpBeforeConfig", name)
			}
		})
	}
}

func bundleHelpExists(t *testing.T) bool {
	t.Helper()
	cfg, err := config.LoadFresh()
	require.NoError(t, err)
	_, err = operations.GetBundle(cfg, "help")
	return err == nil
}

func agentHelpExists(t *testing.T) bool {
	t.Helper()
	cfg, err := config.LoadFresh()
	require.NoError(t, err)
	_, ok := cfg.Agent("help")
	return ok
}
