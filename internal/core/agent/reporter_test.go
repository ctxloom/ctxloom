package agent

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The engine base reports to the Reporter each call is handed — two
// lifecycles in one process (two runs on one runner, two materializations in
// one CLI) never share a channel. MergeHooksConfig's nil-dest drop is the
// deterministic trigger.
func TestMergeHooksConfig_ReportsToTheReporterItIsHanded(t *testing.T) {
	var a, b report.Collector
	src := &wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "echo"}}}}

	MergeHooksConfig(report.To(&a), nil, src)

	require.Len(t, a.All(), 1, "the drop reaches the Reporter it was handed")
	assert.Contains(t, a.All()[0].Text, "hook merge has no destination hook set")
	assert.Empty(t, b.All(), "a Reporter nobody handed in hears nothing")
}

// The managed writers report through the option the engine passes in; an
// unsafe item name is skipped and named on THAT Reporter (WithWriteReporter).
func TestWriteManagedPackageFiles_SkipsReportThroughWithReporter(t *testing.T) {
	var found report.Collector
	fs := afero.NewMemMapFs()
	_, err := WriteManagedSkillPackages(safefs.NewMem(fs), "/work/skills", []SkillExport{{Name: "../escape", Enabled: true}}, WithWriteReporter(&found))
	require.NoError(t, err)
	require.Len(t, found.All(), 1)
	assert.Contains(t, found.All()[0].Text, `skipping package "../escape"`)
}
