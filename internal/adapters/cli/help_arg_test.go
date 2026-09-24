package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// helpArgBehaviour is what a `<verb> <name>` command does when the name it is
// given is literally "help". There are two answers in this package and the
// difference between them is the whole point of this test.
type helpArgBehaviour int

const (
	// helpAsFallback: the command loads config and looks the resource up
	// first, and renders help only when there is no such resource — where the
	// command was going to fail anyway, so the courtesy costs nothing. A
	// bundle, agent or profile literally named "help" is honoured instead.
	//
	// These commands do not have the no-config property in the strict sense:
	// they reach GetConfig() before they can know whether the shortcut
	// applies, and config resolution has a side effect (findAppDir creates
	// ~/.ctxloom when it falls back to the home layer). What the user actually
	// relied on survives — `ctxloom bundle show help` in a directory with no
	// ctxloom config still prints help and exits 0 — because a config-less
	// load succeeds with an empty config rather than erroring. That is what
	// the assertions below check: help still renders with no config present.
	helpAsFallback helpArgBehaviour = iota

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
// is meant to be exhaustive: every command that calls helpFallback appears
// here, plus the create commands that deliberately do not. Nothing derives it
// from the source; a new name-taking command has to be added by hand.
var helpArgCommands = []struct {
	path      []string
	behaviour helpArgBehaviour
	// exists reports whether the resource named "help" is now present.
	// Required for (and used only by) actsOnResource rows.
	exists func(t *testing.T) bool
	// flags are set on the command before RunE, for a resource whose create
	// needs more than a name: an agent binds an engine or it is refused
	// (operations.ErrAgentWithoutEngine), so the row that proves "help" is a
	// NAME here must still supply a binding. Profile names are not resolved
	// at write time, so the no-config premise of the test holds; nor are a
	// profile's bundle refs, so `profile create` needs only one to be accepted.
	flags map[string]string
	// seed prepares the project before RunE. An agent is a fact about the
	// PROJECT (layerscope: every agents.* field is ScopeShared), so a create
	// with no project config lands in the home layer, where the binding is
	// dropped on load; the row that proves "help" is a NAME here needs a
	// project config to write into.
	seed func(t *testing.T)
}{
	{path: []string{"bundle", "create"}, behaviour: actsOnResource, exists: bundleHelpExists},
	{path: []string{"bundle", "edit"}, behaviour: helpAsFallback},
	{path: []string{"bundle", "show"}, behaviour: helpAsFallback},
	{path: []string{"agent", "show"}, behaviour: helpAsFallback},
	{path: []string{"agent", "create"}, behaviour: actsOnResource, exists: agentHelpExists, flags: map[string]string{"profiles": "default"}, seed: seedProjectConfig},
	{path: []string{"agent", "default"}, behaviour: helpAsFallback},
	{path: []string{"agent", "remove"}, behaviour: helpAsFallback},
	{path: []string{"profile", "create"}, behaviour: actsOnResource, exists: profileHelpExists, flags: map[string]string{"bundle": "some-bundle"}},
	{path: []string{"profile", "remove"}, behaviour: helpAsFallback},
	{path: []string{"profile", "show"}, behaviour: helpAsFallback},
	{path: []string{"profile", "modify"}, behaviour: helpAsFallback},
}

// TestHelpArgShortcut_BehaviourForEveryNameTakingCommand pins the arg-position
// "help" behaviour across every name-taking command at the PUBLIC SEAM (a real
// command invocation) rather than against helpFallback itself: what is pinned
// is where each command consults it, which the helper cannot see.
//
// The assertion is per-behaviour, not shared. A create that stopped printing
// help could satisfy "help was not rendered" by doing nothing at all; pinning
// what it does INSTEAD is what makes a revert red.
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
			resetApp()
			t.Cleanup(resetApp)
			if tc.seed != nil {
				tc.seed(t)
			}
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
			for k, v := range tc.flags {
				require.NoError(t, cmd.Flags().Set(k, v))
			}
			t.Cleanup(func() {
				cmd.SetOut(nil)
				cmd.SetContext(context.Background())
				for k := range tc.flags {
					resetFlag(t, cmd, k)
				}
			})

			require.NoError(t, cmd.RunE(cmd, []string{"help"}),
				"`ctxloom %s help` must succeed", name)

			switch tc.behaviour {
			case helpAsFallback:
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

			// Config is loaded before the shortcut can apply, and
			// config.findAppDir CREATES ~/.ctxloom when it falls back to the
			// home layer — so its presence afterwards is proof the command
			// resolved config first, i.e. that "help" was looked up as a name
			// before it was read as a request for help.
			if tc.seed == nil && tc.behaviour == helpAsFallback {
				_, statErr := os.Stat(filepath.Join(home, ".ctxloom"))
				assert.NoError(t, statErr,
					"`ctxloom %s help` must look the name up before reading it as a help request", name)
			}
		})
	}
}

// resetFlag restores a flag the row set. Set(k, "") does NOT do this for a
// slice flag: once changed, pflag's slice Set APPENDS, and an empty value
// appends nothing, so the row's value would leak into every later test that
// runs the same command.
func resetFlag(t *testing.T, cmd *cobra.Command, name string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	require.NotNil(t, f, "flag --%s", name)
	if sv, ok := f.Value.(interface{ Replace([]string) error }); ok {
		require.NoError(t, sv.Replace(nil))
	} else {
		require.NoError(t, f.Value.Set(f.DefValue))
	}
	f.Changed = false
}

func profileHelpExists(t *testing.T) bool {
	t.Helper()
	cfg, err := configload.Load()
	require.NoError(t, err)
	_, err = operations.GetProfile(context.Background(), cfg, operations.GetProfileRequest{Name: "help"})
	return err == nil
}

func bundleHelpExists(t *testing.T) bool {
	t.Helper()
	cfg, err := configload.Load()
	require.NoError(t, err)
	_, err = operations.GetBundle(cfg, "help")
	return err == nil
}

// seedProjectConfig gives the project dir a minimal config so a project-scoped
// write has a project layer to land in.
func seedProjectConfig(t *testing.T) {
	t.Helper()
	require.NoError(t, os.MkdirAll(".ctxloom", 0o755))
	testsupport.WriteFileString(t, afero.NewOsFs(), filepath.Join(".ctxloom", "config.yaml"), "version: 6\n", 0o644)
}

func agentHelpExists(t *testing.T) bool {
	t.Helper()
	cfg, err := configload.Load()
	require.NoError(t, err)
	_, ok := cfg.Agent("help")
	return ok
}
