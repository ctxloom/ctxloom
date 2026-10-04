package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// framingCommandSpan matches a backticked ctxloom invocation in the framing
// text, capturing everything after the binary name.
var framingCommandSpan = regexp.MustCompile("`ctxloom ([^`]+)`")

// The framing preamble is delivered to every agent at session start, so a
// command it names that the CLI lacks sends every agent to an unknown command.
// Each backticked `ctxloom ...` span must resolve in the real command tree with
// no word left over, and each --flag it shows must exist on that command.
func TestProjectContextPreamble_NamesOnlyRealCommands(t *testing.T) {
	root := GetRootCmd(testComposition())
	spans := framingCommandSpan.FindAllStringSubmatch(agent.ProjectContextPreamble, -1)
	require.NotEmpty(t, spans, "the preamble names ctxloom commands")

	for _, span := range spans {
		var words, flags []string
		for _, tok := range strings.Fields(span[1]) {
			switch {
			case strings.HasPrefix(tok, "<"):
			case strings.HasPrefix(tok, "--"):
				flags = append(flags, strings.TrimPrefix(tok, "--"))
			default:
				words = append(words, tok)
			}
		}
		cmd, rest, err := root.Find(words)
		require.NoError(t, err, "`ctxloom %s`", span[1])
		assert.Empty(t, rest, "`ctxloom %s` names words the command tree does not have", span[1])
		for _, f := range flags {
			assert.NotNil(t, cmd.Flags().Lookup(f), "`ctxloom %s`: %s has no --%s flag", span[1], cmd.CommandPath(), f)
		}
	}
}
