package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// A bundle hook narrows a tool event by a neutral tool CLASS, never by an
// engine's own tool names: each engine's hooks approach maps the class to
// its native matcher at delivery (agent.BindHooks), so the same bundle works
// under every engine.
func TestParseBundle_AHookDeclaresAToolClass(t *testing.T) {
	b, err := ParseBundle([]byte("version: \"1.0.0\"\nhooks:\n  pre_tool:\n    - command: guard\n      tool: shell\n    - command: guard\n      tool: file_edit\n"))
	require.NoError(t, err)
	require.Len(t, b.Hooks.PreTool, 2)
	assert.Equal(t, wire.ToolShell, b.Hooks.PreTool[0].Tool)
	assert.Equal(t, wire.ToolFileEdit, b.Hooks.PreTool[1].Tool)
}

// The engine-native matcher is not a bundle key: a bundle naming claude's
// tools (`matcher: Bash`) would fire under claude alone and match nothing,
// or the wrong thing, anywhere else.
//
// MUTATION -- restore BundleHook.Matcher -- turns this red.
func TestParseBundle_RefusesAnEngineNativeMatcher(t *testing.T) {
	_, err := ParseBundle([]byte("version: \"1.0.0\"\nhooks:\n  pre_tool:\n    - command: guard\n      matcher: Bash\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matcher")
}

// A class nothing maps is refused at parse, naming the vocabulary: delivered,
// it would fail every engine's binding far from the typo.
//
// MUTATION -- drop checkHookTools from ParseBundle -- turns this red.
func TestParseBundle_RefusesAnUnknownToolClass(t *testing.T) {
	_, err := ParseBundle([]byte("version: \"1.0.0\"\nhooks:\n  pre_tool:\n    - command: guard\n      tool: Bash\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"Bash"`)
	for _, c := range wire.ToolClasses {
		assert.Contains(t, err.Error(), string(c), "the refusal names the classes a hook may use")
	}
}
