package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
)

// TestLoadoutCmd_IsOnTheRoot: `ctxloom loadout` is an ordinary command on
// the documented tree — ctxloom is its own companion and answers the same
// probe every companion answers.
func TestLoadoutCmd_IsOnTheRoot(t *testing.T) {
	sub, _, err := GetRootCmd(testComposition()).Find([]string{loadout.Subcommand})
	require.NoError(t, err)
	assert.Equal(t, loadout.Subcommand, sub.Name())
}

// TestLoadoutCmd_EmitsTheCompositionsLoadout: the bytes come from the
// composition root (cmd/ctxloom embeds them); the command itself carries
// none, so a process composed without a loadout fails LOUD rather than
// emitting an empty document.
func TestLoadoutCmd_EmitsTheCompositionsLoadout(t *testing.T) {
	doc := []byte("run:\n  version: 1.0.0\n")
	comp := testComposition()
	comp.Loadout = EmbeddedLoadout{YAML: doc}

	var out bytes.Buffer
	require.Equal(t, 0, RunWithArgs(comp, []string{loadout.Subcommand, "--" + loadout.FormatFlag, loadout.FormatYAML}, &out), out.String())
	assert.Equal(t, doc, out.Bytes())

	var empty bytes.Buffer
	assert.NotEqual(t, 0, RunWithArgs(testComposition(), []string{loadout.Subcommand}, &empty),
		"a composition carrying no loadout must fail loud, never emit an empty envelope")
}
