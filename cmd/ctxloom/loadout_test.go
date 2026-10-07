package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestLoadout_YAML_IsAValidLoadout proves ctxloom's own embedded loadout.yaml
// parses as a loadout document whose RUN bundle carries everything ctxloom
// delivers into an engine on its own behalf: its MCP server entry — the
// companion's DYNAMIC declaration, served by the running session's endpoint
// (wire.ServedBySessionEndpoint) with nothing executable on it, so that at
// rest the entry renders nothing and inside a session the engine's dynamic
// approach renders the endpoint — and the always-on isolation-axes fragment.
func TestLoadout_YAML_IsAValidLoadout(t *testing.T) {
	lo, err := bundles.ParseLoadout(loadoutYAML)
	require.NoError(t, err, "ctxloom's loadout.yaml must be a well-formed loadout document")
	assert.True(t, lo.Init.IsZero(), "ctxloom declares no INIT loadout today; a typed field appearing here is a content change to review")
	b := lo.Run

	require.Contains(t, b.MCP, agent.MCPServerName, "loadout must carry ctxloom's own MCP server entry")
	entry := b.MCP[agent.MCPServerName]
	assert.Equal(t, wire.ServedBySessionEndpoint, entry.ServedBy, "ctxloom's entry is served by the running session's endpoint")
	assert.Empty(t, entry.Command, "a dynamic entry names no command: there is no stdio server to launch")
	assert.Empty(t, entry.Args)
	assert.NotContains(t, entry.Notes, "stdio", "the notes describe the entry as it is")

	require.Contains(t, b.Fragments, "isolation-axes", "loadout must carry the always-on isolation guidance")
	assert.Empty(t, b.Fragments["isolation-axes"].Premise, "isolation guidance is unconditional")
}

// TestCompose_CarriesTheEmbeddedLoadout pins the seam through which the
// embedded bytes reach `ctxloom loadout`: the composition root hands them
// to the CLI, which owns the command (so the documented tree carries it)
// but not the content (go:embed cannot reach outside this package).
func TestCompose_CarriesTheEmbeddedLoadout(t *testing.T) {
	comp := compose(strictness.Sink("ctxloom"), safefs.New())
	assert.Equal(t, loadoutYAML, comp.Loadout.YAML)
}

// loadoutCommandSpan finds a `ctxloom ...` code span in fragment prose; a span
// may wrap across lines.
var loadoutCommandSpan = regexp.MustCompile("`ctxloom\\s+([^`]+)`")

// TestLoadout_FragmentsNameCommandsThatExist: guidance the loadout delivers
// into every session tells agents what to run, so every `ctxloom <noun>
// <verb>` it names must resolve to a real command, not stop at a group whose
// subcommands do not include the verb.
func TestLoadout_FragmentsNameCommandsThatExist(t *testing.T) {
	lo, err := bundles.ParseLoadout(loadoutYAML)
	require.NoError(t, err)
	root := cli.GetRootCmd(compose(strictness.Sink("ctxloom"), safefs.New()))
	for name, frag := range lo.Run.Fragments {
		for _, m := range loadoutCommandSpan.FindAllStringSubmatch(frag.Content, -1) {
			var words []string
			for _, w := range strings.Fields(m[1]) {
				if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") {
					break
				}
				words = append(words, w)
			}
			cmd, rest, err := root.Find(words)
			require.NoErrorf(t, err, "fragment %q names %q", name, m[0])
			if cmd.HasSubCommands() && len(rest) > 0 {
				assert.Failf(t, "unknown command", "fragment %q names %q: %q has no subcommand %q",
					name, m[0], cmd.CommandPath(), rest[0])
			}
		}
	}
}
