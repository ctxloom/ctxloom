package agent

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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

// A chunker fed an oversized single line reports to its Reporter.
func TestChunkContext_OversizedLineReportsToTheReporter(t *testing.T) {
	var found report.Collector
	chunks := ChunkContext(report.To(&found), strings.Repeat("x", ContextChunkMaxChars+1))
	require.NotEmpty(t, chunks)
	require.Len(t, found.All(), 1)
	assert.Contains(t, found.All()[0].Text, "exceeds")
}

// The managed writers report through the option the engine passes in; an
// unsafe item name is skipped and named on THAT Reporter (WithWriteReporter).
func TestWriteManagedPackageFiles_SkipsReportThroughWithReporter(t *testing.T) {
	var found report.Collector
	fs := afero.NewMemMapFs()
	err := WriteManagedSkillPackages(fs, "/work/skills", []SkillExport{{Name: "../escape", Enabled: true}}, WithWriteReporter(&found))
	require.NoError(t, err)
	require.Len(t, found.All(), 1)
	assert.Contains(t, found.All()[0].Text, `skipping package "../escape"`)
}
