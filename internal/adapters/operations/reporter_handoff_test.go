package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// TestApplyHooks_ReportsThroughTheGenerationsReporter pins that a finding
// raised while delivering hooks reaches the Reporter the composition root
// handed the config owner (cfg.Reporter()), not a terminal channel operations
// reaches for itself. The context-injection hook warns when its cached
// context file cannot be read; a hash with no cache file forces that warning.
func TestApplyHooks_ReportsThroughTheGenerationsReporter(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := cfgWithProfileHooks(t, fs, "/project/.ctxloom", wire.HooksConfig{}, config.Fixture{})
	var got report.Findings
	cfg.SetReporter(&got)

	_, err := applyHooksToBackend(context.Background(), engines.Registry(), "claude-code", hookApplyParams{
		freshCfg:    cfg,
		workDir:     t.TempDir(),
		contextHash: "0000000000000000",
		pkg:         composite.Package{},
		fs:          fs,
		dryRun:      true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, got, "the context-injection warning must reach the generation's Reporter")
}
