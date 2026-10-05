package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// helpCommandRef matches a command a reader is told to TYPE: a
// `ctxloom <word>...` opening a backtick or single quote, or an indented
// example line. The words stop at the first operand or flag. A bare "ctxloom"
// in a sentence is the product's name, not a command, and is not matched.
var helpCommandRef = regexp.MustCompile("(?m)(?:^[ \t]+|[`'])ctxloom((?: [a-z][a-z0-9-]*)+)")

// staleHelpCommandRefs returns every `ctxloom ...` mention in help text that
// does not resolve in the real command tree: a word sequence that stops at a
// command with subcommands while words remain. A leaf with words left over is
// a leaf taking operands, which is fine.
func staleHelpCommandRefs(root *cobra.Command, text string) []string {
	var stale []string
	for _, m := range helpCommandRef.FindAllStringSubmatch(text, -1) {
		words := strings.Fields(m[1])
		found, rest, err := root.Find(words)
		if err != nil || (found.HasSubCommands() && len(rest) > 0) {
			stale = append(stale, "ctxloom"+m[1])
		}
	}
	return stale
}

// TestHelpText_NamesOnlyCommandsThatExist is the checked binding between help
// prose and the command tree: a renamed or retired verb fails here, at the
// moment it is renamed, instead of leaving help that sends a user to a
// command that is not there.
func TestHelpText_NamesOnlyCommandsThatExist(t *testing.T) {
	root := rootCommand()
	walkCommands(root, func(c *cobra.Command) {
		for _, ref := range staleHelpCommandRefs(root, c.Long+"\n"+c.Example) {
			t.Errorf("%q help names %q, which is not a command", c.CommandPath(), ref)
		}
	})
}

// TestRootHelp_FirstStepIsInit: the first screen a new user sees opens its
// quick start with the command that sets a project up — every other command
// assumes one exists.
func TestRootHelp_FirstStepIsInit(t *testing.T) {
	refs := helpCommandRef.FindAllStringSubmatch(rootCommand().Long, -1)
	if len(refs) == 0 {
		t.Fatal("the root help names no command to run")
	}
	if got := "ctxloom" + refs[0][1]; got != rootFirstStep {
		t.Errorf("root help's first command is %q, want %q", got, rootFirstStep)
	}
}

// TestFormatFlagHelp_StatesTheDerivedDefault: with --format unset the output
// is text on a terminal and json otherwise (cliemit.Resolve). The help must
// say that, not advertise a fixed "text" default a piped run contradicts.
func TestFormatFlagHelp_StatesTheDerivedDefault(t *testing.T) {
	usage := rootCommand().PersistentFlags().FlagUsages()
	if strings.Contains(usage, `(default "`+formatText+`")`) {
		t.Errorf("--format help advertises a fixed default:\n%s", usage)
	}
	if !strings.Contains(usage, formatFlagUsage) {
		t.Errorf("--format help does not state the derived default:\n%s", usage)
	}
}
