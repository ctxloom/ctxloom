package vendorreader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionInfoBuilder_NothingFoundIsNil(t *testing.T) {
	var b SessionInfoBuilder
	assert.Nil(t, b.Build())

	// Setting to a zero/empty value must not count as "found."
	b.SetSessionID("")
	b.SetModel("")
	b.SetPermissionMode("")
	b.SetContextWindow(0)
	assert.Nil(t, b.Build())
}

func TestSessionInfoBuilder_LatchesFirstNonEmptyOnly(t *testing.T) {
	var b SessionInfoBuilder
	b.SetSessionID("first")
	b.SetSessionID("second") // must not overwrite
	b.SetModel("gpt-5.5")
	b.SetPermissionMode("on-request")
	b.SetContextWindow(1000)
	b.SetContextWindow(2000) // must not overwrite

	info := b.Build()
	require.NotNil(t, info)
	assert.Equal(t, "first", info.SessionID)
	assert.Equal(t, "gpt-5.5", info.Model)
	assert.Equal(t, "on-request", info.PermissionMode)
	assert.Equal(t, 1000, info.ContextWindow)
}

func TestSessionInfoBuilder_PartialFieldsStillFound(t *testing.T) {
	var b SessionInfoBuilder
	b.SetModel("only-model")
	info := b.Build()
	require.NotNil(t, info)
	assert.Equal(t, "only-model", info.Model)
	assert.Empty(t, info.SessionID)
}
