package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

func TestValidateTerminalUIConfig_BadKeyIsFatalFinding(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})

	mark := strictness.Checkpoint()
	cfg := &config.Config{UI: config.UIConfig{PrefixKey: "ctrl-["}} // ESC: rejected
	validateTerminalUIConfig(cfg)

	found := strictness.Since(mark)
	require.Len(t, found, 1, "an invalid prefix key must be a collected startup finding")
	assert.Equal(t, strictness.ClassConfig, found[0].Class)
	assert.Contains(t, found[0].Message, "ui.prefix_key")
}

func TestValidateTerminalUIConfig_DefaultAndValidKeysPass(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)

	mark := strictness.Checkpoint()
	validateTerminalUIConfig(&config.Config{}) // default ctrl-]
	validateTerminalUIConfig(&config.Config{UI: config.UIConfig{PrefixKey: "ctrl-t"}})
	assert.Empty(t, strictness.Since(mark))
}
