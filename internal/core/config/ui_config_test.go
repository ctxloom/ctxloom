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
