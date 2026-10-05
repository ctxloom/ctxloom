package cli

import (
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

// exampleWords splits one example line the way a shell would, enough for
// these lines: single and double quotes group, a " #" starts a comment, and
// only the first command of a "&&" or "|" chain is kept.
func exampleWords(line string) []string {
	var words []string
	var cur strings.Builder
	inWord, quote := false, rune(0)
	for _, r := range strings.TrimSpace(line) {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case r == '#' && !inWord:
			return words
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	for i, w := range words {
		if w == "&&" || w == "|" || w == ";" {
			return words[:i]
		}
	}
	return words
}

// splitExampleFlags separates rest into positional arguments and the flags
// cmd does not take, consuming the value of every non-boolean flag.
func splitExampleFlags(cmd *cobra.Command, rest []string) (positional, unknown []string) {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if w == "--" {
			return append(positional, rest[i+1:]...), unknown
		}
		if !strings.HasPrefix(w, "-") || w == "-" {
			positional = append(positional, w)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(w, "-"), "=")
		var f *pflag.Flag
		if strings.HasPrefix(w, "--") {
			f = cmd.Flags().Lookup(name)
			if f == nil {
				f = cmd.InheritedFlags().Lookup(name)
			}
		} else {
			f = cmd.Flags().ShorthandLookup(name[:1])
			if f == nil {
				f = cmd.InheritedFlags().ShorthandLookup(name[:1])
			}
			hasValue = hasValue || len(name) > 1
		}
		if f == nil {
			unknown = append(unknown, w)
			continue
		}
		if f.NoOptDefVal == "" && !hasValue {
			i++ // the flag's value
		}
	}
	return positional, unknown
}
