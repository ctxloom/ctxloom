package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// The fallback a fault-tolerant command gets when the config cannot load says
// the CONFIG failed, naming the load error.
func TestLoadConfigOrFallback_SaysTheConfigFailedToLoad(t *testing.T) {
	var w bytes.Buffer
	cfg := loadConfigOrFallback(func() (*config.Config, error) {
		return nil, errors.New("yaml: line 3: did not find expected key")
	}, &w)
	require.NotNil(t, cfg)

	msg := w.String()
	assert.Contains(t, msg, "failed to load config")
	assert.Contains(t, msg, "did not find expected key", "the load error itself is named")
}
