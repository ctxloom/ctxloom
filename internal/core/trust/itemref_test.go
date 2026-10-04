package trust

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatSelector_IsParseSelectorsInverse: FormatSelector is the ONE
// selector renderer, and it is the exact inverse of ParseSelector over every
// kind — a minted selector parses back to the kind and name it was minted
// from. A command is minted under its current spelling, "commands/", even
// though its kind's stored directory (ItemKind.Dir) is "prompts".
func TestFormatSelector_IsParseSelectorsInverse(t *testing.T) {
	for _, k := range ItemKinds() {
		for _, name := range []string{"x", "pre_tool/0"} {
			gotKind, gotName, err := ParseSelector(FormatSelector(k, name))
			require.NoError(t, err, "%s/%s", k, name)
			assert.Equal(t, k, gotKind)
			assert.Equal(t, name, gotName)
		}
	}
	assert.Equal(t, "commands/review", FormatSelector(KindPrompt, "review"))
	assert.Equal(t, "fragments/go", FormatSelector(KindFragment, "go"))
}
