package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelpAffordance_HelpFlagReachesEveryCommand is the "always present"
// invariant, and the only one that holds with NO exceptions: --help reaches
// every command in the tree, leaves included.
//
// It is safe to assert everywhere precisely because a flag cannot collide with
// an operand. A bare `help` word competes for the same slot as a positional,
// so on a leaf taking open-ended text one of the two has to lose; --help lives
// in a different namespace and takes nothing away from any command.
//
// The lookup is deliberately done WITHOUT executing anything. Cobra creates a
// local --help lazily on the command it is about to run, so a test that
// dispatched first would prove only that cobra initializes the node it
// reached. Finding the flag on an unexecuted node proves it was inherited from
// the root's persistent set, which is what makes it structural.
func TestHelpAffordance_HelpFlagReachesEveryCommand(t *testing.T) {
	root := rootCommand()

	var missing []string
	walkCommands(root, func(c *cobra.Command) {
		if c.InheritedFlags().Lookup("help") == nil && c.Flags().Lookup("help") == nil {
			missing = append(missing, c.CommandPath())
		}
	})

	assert.Empty(t, missing,
		"these commands do not inherit --help; it is a persistent flag on the root, so a gap means the command is detached from the root's flag chain: %v",
		missing)
}

// TestHelpAffordance_ShorthandStaysAvailable pins -h alongside it. The two are
// one affordance in users' hands, and a persistent --help that dropped the
// shorthand would silently retire the spelling most people type.
func TestHelpAffordance_ShorthandStaysAvailable(t *testing.T) {
	flag := rootCommand().PersistentFlags().Lookup("help")

	require.NotNil(t, flag, "--help is registered on the root's persistent flags")
	assert.Equal(t, "h", flag.Shorthand, "-h stays the shorthand for --help")
}

// TestHelpAffordance_DoesNotLeakIntoTheNextCommand guards the hazard a
// PERSISTENT --help introduces that a per-command one cannot: one shared
// variable, read by every command in the process.
//
// Left set by an earlier invocation, it makes the next command print help and
// exit 0 while doing none of the work it was asked to do — a success status
// over an empty result, which is this project's characteristic failure and
// invisible to any exit-code-only assertion. Asserting on the OUTPUT is what
// catches it.
func TestHelpAffordance_DoesNotLeakIntoTheNextCommand(t *testing.T) {
	remoteBareFixture(t)

	_, err := runRoot(t, "remote", "--help")
	require.NoError(t, err)

	after, err := runRoot(t, "remote", "list")

	require.NoError(t, err)
	assert.NotContains(t, after, usageMarker,
		"a --help on one command must not turn the next one into a no-op that prints help")
}

// TestHelpSuffix_ResolvesOnEveryNamespace is the gate that keeps "always" from
// decaying into "mostly".
//
// The suffix is what pays for a bare noun listing instead of teaching, so a
// single namespace without it takes the teaching surface away from that noun
// and puts it nowhere. A newcomer cannot know in advance which commands are
// generous; an affordance present on most of them is one they must test for.
//
// This walks the assembled tree rather than a hand-kept list, so a namespace
// added tomorrow is covered the moment it exists.
func TestHelpSuffix_ResolvesOnEveryNamespace(t *testing.T) {
	root := rootCommand()

	var missing []string
	walkCommands(root, func(c *cobra.Command) {
		if !c.HasSubCommands() || isHelpSuffix(c) {
			return
		}
		if findHelpSuffix(c) == nil {
			missing = append(missing, c.CommandPath())
		}
	})

	assert.Empty(t, missing,
		"these namespaces answer nothing for '<command> help'; installHelpSuffix walks the tree, so a gap here means the node is not reachable from the root: %v",
		missing)
}

// TestHelpSuffix_TeachesTheCommandItIsAppendedTo drives the suffix through
// cobra's real dispatch at three depths and requires each one to describe the
// command the caller actually named.
//
// Anchoring is the whole risk: cobra's own default help command resolves its
// arguments against the ROOT, so a nested copy answers every namespace with
// the root's help — present, uniform, and useless. Asserting only that help
// text appeared would pass against exactly that.
func TestHelpSuffix_TeachesTheCommandItIsAppendedTo(t *testing.T) {
	for _, args := range [][]string{
		{"remote", "help"},
		{"trust", "signer", "help"},
		{"mcp", "help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := runRoot(t, args...)

			require.NoError(t, err)
			assert.Contains(t, out, usageAnchor(args[:len(args)-1]...),
				"'%s' describes the command it was appended to, not an ancestor",
				strings.Join(args, " "))
		})
	}
}

// TestHelpSuffix_TeachesANamedDescendant pins the argument form: the suffix
// composes down the tree the way `git help <cmd>` does, and resolves the name
// relative to the command it hangs off.
func TestHelpSuffix_TeachesANamedDescendant(t *testing.T) {
	out, err := runRoot(t, "trust", "help", "signer")

	require.NoError(t, err)
	assert.Contains(t, out, usageAnchor("trust", "signer"),
		"'trust help signer' resolves 'signer' under trust")
}

// usageAnchor is the usage line cobra prints for the namespace at path, and it
// names that command and no other. Command help renders Long when a command
// has one, so a Short is not present in the output to assert on.
func usageAnchor(path ...string) string {
	return "ctxloom " + strings.Join(path, " ") + " [command]"
}

// TestHelpSuffix_RefusesAStructuredFormat holds the same line group nodes hold.
// Help is prose; a caller who asked for json to parse must be told it is not
// coming rather than handed prose with a success status.
func TestHelpSuffix_RefusesAStructuredFormat(t *testing.T) {
	_, err := runRoot(t, "remote", "help", "--format", "json")

	require.Error(t, err, "a structured --format aimed at help is refused, not silently ignored")
	assert.Contains(t, err.Error(), "json")
}
