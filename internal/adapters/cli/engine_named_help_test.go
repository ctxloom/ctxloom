package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The help and flag defaults that name engines are computed from the registry
// the Composition hands the tree, not from an App composed at construction
// time: a tree assembled for documentation (GetRootCmd, as scripts/gendocs
// does) renders them exactly as the dispatched binary does.
func TestEngineNamedHelp_ComesFromTheHandedRegistry(t *testing.T) {
	comp := testComposition()
	root := GetRootCmd(comp)
	def := operations.DefaultEngineName(comp.Engines)
	require.NotEmpty(t, def, "the shipped registry names a default engine")
	pickable := operations.EngineNamesWhere(comp.Engines, func(d engine.Definition) bool { return d.Distribution != engine.DistributionTestOnly })
	require.NotEmpty(t, pickable)

	install, _, err := root.Find([]string{"manage", "install"})
	require.NoError(t, err)
	flag := install.Flags().Lookup("engine")
	require.NotNil(t, flag)
	assert.Equal(t, def, flag.DefValue, "`manage install --engine` defaults to the shipped default engine")
	assert.Contains(t, install.Flags().FlagUsages(), `(default "`+def+`")`, "the rendered help states that default")

	edit, _, err := root.Find([]string{"llm", "edit"})
	require.NoError(t, err)
	typeHelp := edit.Flags().Lookup("type").Usage
	for _, name := range pickable {
		assert.Contains(t, typeHelp, name, "`llm edit --type` names every pickable engine")
		assert.Contains(t, edit.Long, name, "`llm edit` long help names every pickable engine")
	}
	assert.Contains(t, typeHelp, "(empty = "+def+")", "the --type help names the default engine")
	assert.False(t, strings.Contains(typeHelp, ":  ("), "the engine list is never empty")
}
