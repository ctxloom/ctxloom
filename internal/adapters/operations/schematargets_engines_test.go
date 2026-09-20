//go:build schemagen

package operations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// Every registered engine that declares an export-block schema is
// published under "engine-exports-<name>", and claude-code's is the very
// document its decoder validates with.
func TestEngineExportSchemaTargets_PublishEveryRegisteredEngine(t *testing.T) {
	targets := EngineExportSchemaTargets()
	byName := map[string][]byte{}
	for _, tg := range targets {
		require.Nil(t, tg.Type, "an engine's schema is authored, not reflected")
		byName[tg.Name] = tg.Schema
	}
	reg, err := engines.Build()
	require.NoError(t, err)
	for _, name := range reg.Names(nil) {
		assert.Contains(t, byName, "engine-exports-"+string(name), "every shipped engine publishes its export schema")
	}
	assert.Equal(t, string(claude.ExportSchema), string(byName["engine-exports-"+claude.EngineName]))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(byName["engine-exports-"+claude.EngineName], &doc))
	assert.Equal(t, "object", doc["type"])
	assert.Contains(t, SchemaTargets(), targets[0], "the engine targets ride the same list gen-schemas reads")
}
