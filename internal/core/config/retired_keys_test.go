package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The init path (ParseConfig) skips schema validation, so without this a
// retired key in a document it reads would vanish in silence. It records the
// same unknown-key warning, from the same table, that the main load does.
func TestParseConfig_RecordsRetiredKeys(t *testing.T) {
	cfg, err := ParseConfig([]byte("schema_version: 6\nisolation_devcontainer_base: false\nllm:\n  plugins: {}\n"))
	require.NoError(t, err)

	var texts []string
	for _, w := range cfg.GetWarnings() {
		assert.Equal(t, WarnKindUnknownKey, w.Kind)
		texts = append(texts, w.Text)
	}
	require.Len(t, texts, 2, "one warning per retired key, nested paths included: %v", texts)
	want0, _ := RetiredKeyMessage("isolation_devcontainer_base", parsedDocumentSource)
	want1, _ := RetiredKeyMessage("llm.plugins", parsedDocumentSource)
	assert.ElementsMatch(t, []string{want0, want1}, texts, "the same rendered line the main load prints")
	assert.Contains(t, want0, "isolation_base")
}

func TestParseConfig_CleanDocumentRecordsNoWarnings(t *testing.T) {
	cfg, err := ParseConfig([]byte("schema_version: 6\nisolation_base: ctxloom\n"))
	require.NoError(t, err)
	assert.Empty(t, cfg.GetWarnings())
}

func TestRetiredKeyMessage_UnknownPathIsNotRetired(t *testing.T) {
	_, ok := RetiredKeyMessage("isolation_base", "x")
	assert.False(t, ok)
}
