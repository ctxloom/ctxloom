package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestCompactionModelFor_ModelOverrideBeatsConfig closes sour-scoop: an
// explicit caller-supplied model reached the backend distill path but NOT the
// canonical/harp one, which always used cfg.GetCompactionModel(). The caller
// got a distill from a model it did not ask for, silently. CompactEntry now
// takes the override directly, with "" meaning "use the configured model" —
// the same shape distillSession already uses.
func TestCompactionModelFor_ModelOverrideBeatsConfig(t *testing.T) {
	cfg := &config.Config{}
	configured := cfg.GetCompactionModel()

	assert.Equal(t, "sonnet-override", CompactionModelFor(cfg, "sonnet-override"),
		"an explicit override must win over the configured model")
	assert.Equal(t, configured, CompactionModelFor(cfg, ""),
		"an empty override must fall back to the configured model")
}
