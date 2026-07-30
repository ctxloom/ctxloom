package mcpschema

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U026-F07 (aliasing half): Tools() memoises its result, so handing callers the
// package-level slice let one caller's edit rewrite the tool surface every
// later caller sees — including the runner's registration loop, which is the
// only thing standing between a child and a coordinator-only tool.
func TestTools_CallersCannotMutateTheMemoisedSurface(t *testing.T) {
	first, err := Tools()
	require.NoError(t, err)
	require.NotEmpty(t, first)

	original := first[0]
	first[0].Name = "hijacked"
	first[0].Description = "hijacked"
	first[0].InputSchema[0] = 'X'

	second, err := Tools()
	require.NoError(t, err)
	assert.Equal(t, original.Name, second[0].Name, "a caller's edit must not rename a tool")
	assert.Equal(t, original.Description, second[0].Description)
	assert.Equal(t, byte('{'), second[0].InputSchema[0],
		"a caller's edit must not reach into the memoised schema bytes")
}

// U026-F07 (partial-load half): a corrupt schema used to come back as an error
// AND a truncated tool list. A caller that logs the error and carries on would
// register a silently incomplete surface — this project's characteristic
// failure. Nothing is returned alongside the error now.
func TestLoadTools_ACorruptSchemaYieldsNoPartialSurface(t *testing.T) {
	fsys := fstest.MapFS{
		"schemas/agent_run.json":  {Data: []byte(`{"name":"agent_run","description":"d","inputSchema":{}}`)},
		"schemas/agent_send.json": {Data: []byte(`{"name":"agent_send",`)},
		"schemas/roster.json":     {Data: []byte(`{"name":"roster","description":"d","inputSchema":{}}`)},
	}
	specs, err := loadTools(fsys)
	require.Error(t, err, "a corrupt schema must fail the load")
	assert.Contains(t, err.Error(), "agent_send.json", "the error names the corrupt file")
	assert.Nil(t, specs, "a failed load returns no tools at all, not the ones that parsed")
}

// The healthy path still loads every schema, sorted.
func TestLoadTools_SortsByName(t *testing.T) {
	fsys := fstest.MapFS{
		"schemas/roster.json":    {Data: []byte(`{"name":"roster","description":"d","inputSchema":{}}`)},
		"schemas/agent_run.json": {Data: []byte(`{"name":"agent_run","description":"d","inputSchema":{}}`)},
	}
	specs, err := loadTools(fsys)
	require.NoError(t, err)
	require.Len(t, specs, 2)
	assert.Equal(t, "agent_run", specs[0].Name)
	assert.Equal(t, "roster", specs[1].Name)
}
