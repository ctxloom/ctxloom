package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// helpArgCommands is every `<verb> <name>` command that honours the
// arg-position help shortcut: `ctxloom profile show help` renders the
// command's help instead of looking for something called "help".
//
// The shortcut exists because these commands take a mandatory name argument,
// so cobra's own `ctxloom help profile show` is the only other way in and
// users reliably type the name-position form first.
var helpArgCommands = [][]string{
	{"bundle", "create"},
	{"bundle", "edit"},
	{"bundle", "show"},
	{"agent", "show"},
	{"agent", "set"},
	{"agent", "default"},
	{"agent", "remove"},
	{"profile", "create"},
	{"profile", "delete"},
	{"profile", "show"},
	{"profile", "modify"},
}

// The shared behaviour of the arg-position help shortcut, pinned across all
// eleven sites at the PUBLIC SEAM (a real command invocation) rather than
// against any one copy of the idiom.
//
// The eleven implementations are VERBATIM identical, so a parity test between
// them cannot be red — there is no divergence to find. This test's job is the
// other one template section 4 names for that case: pin the shared behaviour so
// collapsing the copies onto a single helper is provably behaviour-preserving.
// It is unchanged by the collapse and red only if the seam's behaviour moves.
//
// Two properties matter and both are asserted: help is RENDERED (not an
// "unknown item" error), and it is rendered WITHOUT loading config — the
// shortcut returns before GetConfig, which is what lets `... help` work in a
// directory that has no ctxloom config at all.
func TestHelpArgShortcut_RendersHelpForEveryNameTakingCommand(t *testing.T) {
	testsupport.Isolate(t) // no config anywhere: the shortcut must not need one

	for _, path := range helpArgCommands {
		name := path[0] + " " + path[1]
		t.Run(name, func(t *testing.T) {
			// Find() rather than rootCmd.Execute(): Execute() lazily
			// materialises cobra's built-in `help` command onto the root,
			// which the --format coverage walk then reports as an
			// unregistered command. Find is a pure traversal.
			cmd, _, err := rootCmd.Find(path)
			require.NoError(t, err)
			require.NotNil(t, cmd.RunE, "%s must have a RunE to guard", name)

			var out bytes.Buffer
			cmd.SetOut(&out)
			t.Cleanup(func() { cmd.SetOut(nil) })

			require.NoError(t, cmd.RunE(cmd, []string{"help"}),
				"`ctxloom %s help` must render help, not fail looking for an item named \"help\"", name)
			assert.Contains(t, out.String(), "Usage:", "help output must actually be the help")
			assert.Contains(t, out.String(), path[1],
				"the help rendered must be THIS command's, not a parent's")
		})
	}
}
