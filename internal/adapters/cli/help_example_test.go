package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
)

// Every command a user can run directly shows at least one example of
// running it. Hidden commands are hook and plumbing entry points, not typed
// by a person.
func TestHelp_EveryLeafCommandHasAnExample(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		if c.Hidden || c.HasAvailableSubCommands() || !c.Runnable() || c.Name() == "help" {
			return
		}
		assert.NotEmptyf(t, strings.TrimSpace(c.Example), "%q has no Example", c.CommandPath())
	})
}

// Every Example line is a command that would run: it resolves to the command
// it documents (or one beneath it, for a group's examples), names only flags
// that command takes, and passes the command's own argument check.
func TestHelp_ExamplesAreRealCommands(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		for _, line := range strings.Split(c.Example, "\n") {
			words := exampleWords(line)
			if len(words) == 0 {
				continue
			}
			if !assert.Equalf(t, "ctxloom", words[0], "%q example %q does not run ctxloom", c.CommandPath(), line) {
				continue
			}
			found, rest, err := rootCmd.Find(words[1:])
			if !assert.NoErrorf(t, err, "%q example %q", c.CommandPath(), line) {
				continue
			}
			if !assert.Truef(t, found == c || strings.HasPrefix(found.CommandPath(), c.CommandPath()+" "),
				"%q example %q runs %q", c.CommandPath(), line, found.CommandPath()) {
				continue
			}
			positional, bad := splitExampleFlags(found, rest)
			assert.Emptyf(t, bad, "%q example %q names flags %q does not take", c.CommandPath(), line, found.CommandPath())
			assert.NoErrorf(t, found.ValidateArgs(positional), "%q example %q", c.CommandPath(), line)
		}
	})
}

// exampleToken is one shell word of an example line: a single- or
// double-quoted word, or a run of non-space characters.
var exampleToken = regexp.MustCompile(`'[^']*'|"[^"]*"|\S+`)

// exampleWords splits one example line the way a shell would, enough for
// these lines: quotes group a word, an unquoted '#' word starts a comment,
// and only the first command of a "&&", "|" or ";" chain is kept.
func exampleWords(line string) []string {
	var words []string
	for _, tok := range exampleToken.FindAllString(line, -1) {
		if strings.HasPrefix(tok, "#") || tok == "&&" || tok == "|" || tok == ";" {
			break
		}
		words = append(words, strings.Trim(tok, `'"`))
	}
	return words
}

// splitExampleFlags separates rest into positional arguments and the flags
// cmd does not take, consuming the value of every non-boolean flag.
func splitExampleFlags(cmd *cobra.Command, rest []string) (positional, unknown []string) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			positional = append(positional, w)
			continue
		}
		f, inline := lookupExampleFlag(cmd, w)
		if f == nil {
			unknown = append(unknown, w)
			continue
		}
		if f.NoOptDefVal == "" && !inline {
			i++ // the flag's value is the next word
		}
	}
	return positional, unknown
}

// lookupExampleFlag finds the flag a "--name[=v]" or "-x[v]" word names among
// cmd's own and inherited flags, and whether the word carries its value.
func lookupExampleFlag(cmd *cobra.Command, w string) (*pflag.Flag, bool) {
	if name, ok := strings.CutPrefix(w, "--"); ok {
		name, _, inline := strings.Cut(name, "=")
		if f := cmd.Flags().Lookup(name); f != nil {
			return f, inline
		}
		return cmd.InheritedFlags().Lookup(name), inline
	}
	short := strings.TrimPrefix(w, "-")
	if f := cmd.Flags().ShorthandLookup(short[:1]); f != nil {
		return f, len(short) > 1
	}
	return cmd.InheritedFlags().ShorthandLookup(short[:1]), len(short) > 1
}
