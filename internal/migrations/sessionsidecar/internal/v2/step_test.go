package v2

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// A generation-1 sidecar passes through unchanged: it carries no keep mark.
func TestStep_EditsNothing(t *testing.T) {
	in := "schema_version: 1\nproject_dir: /proj\nended_at: 2026-09-01T00:00:00Z\n"
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(in), &doc))
	Step{}.Apply(doc.Content[0])
	out, err := yamlx.Marshal(&doc)
	require.NoError(t, err)
	assert.Equal(t, in, string(out))
}

func TestStep_MigratesToTwo(t *testing.T) {
	assert.Equal(t, 2, Step{}.To())
}
