package v3

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

// The staleness fingerprint goes; every other field stays.
func TestStep_DropsSourceEntries(t *testing.T) {
	assert.Equal(t, "schema_version: 2\nproject_dir: /proj\n",
		apply(t, "schema_version: 2\nproject_dir: /proj\nsource_entries: 12\n"))
	assert.Equal(t, "schema_version: 2\nproject_dir: /proj\n",
		apply(t, "schema_version: 2\nproject_dir: /proj\n"))
}

func TestStep_MigratesToThree(t *testing.T) {
	assert.Equal(t, 3, Step{}.To())
}
