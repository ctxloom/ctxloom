package v8

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

func apply(t *testing.T, in string) string {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(in), &doc))
	Step{}.Apply(doc.Content[0])
	out, err := yamlx.Marshal(&doc)
	require.NoError(t, err)
	return string(out)
}

// The essence budget goes; every sibling setting stays.
func TestStep_DropsEssenceMaxChars(t *testing.T) {
	in := "schema_version: 7\nconfig:\n    use_distilled: false\n    essence_max_chars: 4096\n"
	assert.Equal(t, "schema_version: 7\nconfig:\n  use_distilled: false\n", apply(t, in))
}

// A `config:` section the key alone filled leaves with it; a config without
// the key, or with a malformed section, is left for the strict decode.
func TestStep_EmptiedSectionLeavesAndAbsentKeyIsUntouched(t *testing.T) {
	assert.Equal(t, "schema_version: 7\n", apply(t, "schema_version: 7\nconfig:\n    essence_max_chars: 4096\n"))
	assert.Equal(t, "schema_version: 7\nworkspace: worktree\n", apply(t, "schema_version: 7\nworkspace: worktree\n"))
	assert.Equal(t, "schema_version: 7\nconfig: [x]\n", apply(t, "schema_version: 7\nconfig: [x]\n"))
}

func TestStep_MigratesToEight(t *testing.T) {
	assert.Equal(t, 8, Step{}.To())
}
