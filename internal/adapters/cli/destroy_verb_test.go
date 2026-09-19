package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One verb destroys, and it is spelled `remove` everywhere.
//
// A destroy verb is the one a reader guesses rather than looks up, so a noun
// that spells it differently costs a failed invocation every time. The cost is
// paid at the prompt and never shows up in a per-noun test: each noun's own
// surface test asserts the leaves IT carries, so a noun spelling the verb
// `delete` passes its own test forever while disagreeing with every sibling.
// Only a whole-tree assertion can see that, which is why this reads the SET
// across all nouns instead of checking any one of them.
//
// The absence half is stated second on purpose. `assert.Empty` over a walk
// that silently visited nothing passes while proving nothing, so the positive
// case runs first in the same fixture and pins both the walk's reach and the
// verbs it is supposed to find.
func TestDestroyVerbIsUniformlyRemove(t *testing.T) {
	var removes, deletes []string
	visited := 0

	walkCommands(rootCommand(), func(c *cobra.Command) {
		visited++
		// CommandPath leads with the root's own name; the noun is what follows.
		path := strings.TrimPrefix(c.CommandPath(), "ctxloom ")
		parent := strings.TrimSuffix(path, c.Name())
		for _, name := range append([]string{c.Name()}, c.Aliases...) {
			// Report the spelling that MATCHED, not the command's canonical
			// path — an alias would otherwise be reported under the name it is
			// hiding behind, sending a reader to a command that looks correct.
			spelling := parent + name
			switch name {
			case "remove":
				removes = append(removes, spelling)
			case "delete":
				deletes = append(deletes, spelling)
			}
		}
	})
	sort.Strings(removes)
	sort.Strings(deletes)

	// The walk reached the tree. A traversal that broke and visited a handful
	// of commands would satisfy every assertion below it.
	require.Greater(t, visited, 100,
		"the walk reaches the whole command tree, not just the root's children")

	// The positive case: the verb this project uses, on the nouns that carry it.
	require.NotEmpty(t, removes, "some noun destroys something")
	for _, noun := range []string{
		"agent", "bundle", "command", "fragment",
		"llm", "profile", "remote", "session", "skill",
	} {
		assert.Contains(t, removes, noun+" remove",
			"`ctxloom %s remove` is how the %s noun destroys", noun, noun)
	}

	// The absence half, now that the fixture is known to be looking at something.
	//
	// The line is drawn at the full word. Shorthand aliases (`rm`, `del` on
	// `remote remove`) are abbreviations OF the canonical verb and stay; what
	// this refuses is a second full name that reads as a peer of `remove` and
	// leaves a reader to guess which noun took which.
	assert.Empty(t, deletes,
		"`delete` is a second spelling for `remove`; one destroy verb, everywhere")
}
