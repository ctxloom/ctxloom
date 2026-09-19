package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUIPrefixKey_Default(t *testing.T) {
	var c Config
	assert.Equal(t, "ctrl-]", c.UIPrefixKey())
}

func TestUIPrefixKey_Configured(t *testing.T) {
	cfg, err := ParseConfig([]byte("ui:\n  prefix_key: ctrl-t\n"))
	require.NoError(t, err)
	assert.Equal(t, "ctrl-t", cfg.UIPrefixKey())
}

func TestUISurroundEnabled_DefaultTrue(t *testing.T) {
	var c Config
	assert.True(t, c.UISurroundEnabled())
}

func TestUISurroundEnabled_OptOut(t *testing.T) {
	cfg, err := ParseConfig([]byte("ui:\n  surround: false\n"))
	require.NoError(t, err)
	assert.False(t, cfg.UISurroundEnabled())

	cfg, err = ParseConfig([]byte("ui:\n  surround: true\n"))
	require.NoError(t, err)
	assert.True(t, cfg.UISurroundEnabled())
}
