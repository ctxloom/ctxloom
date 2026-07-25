package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// U083-F17: session/set_mode replaced the session's lead context with
// res.Context UNCHECKED — an assembly that succeeds while producing zero
// bytes silently blanks the mode's context, and the editor is told the mode
// switch worked.
func TestAssembleModeFunc_ZeroByteAssemblyIsRefused(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)

	orig := assembleModeContext
	assembleModeContext = func(context.Context, *config.Config, AssembleContextRequest) (*AssembleContextResult, error) {
		return &AssembleContextResult{Context: "  \n "}, nil
	}
	defer func() { assembleModeContext = orig }()

	assemble := assembleModeFunc(cfg, "claude")
	got, err := assemble(context.Background(), SessionMode{ID: "default", Name: "default"})
	assert.Error(t, err, "an assembly producing zero bytes must not silently blank the session's lead context")
	assert.Empty(t, got)
}

// U083-F06: a listing/assembly FAILURE degraded to nil, which the init
// summary rendered as the authoritative word "none" — the one artifact whose
// job is to say what ctxloom assembled claims it assembled nothing, when the
// truth is it does not know.
func TestBuildSessionInitSummary_FailedListingIsNotNone(t *testing.T) {
	summary := buildSessionInitSummary(sessionInitSummaryInputs{
		backendName:     "claude",
		fragmentsLoaded: listingFailed(assert.AnError),
		commandNames:    listingFailed(assert.AnError),
		skillNames:      listingFailed(assert.AnError),
		mcpServerNames:  listed(nil),
	})
	assert.NotContains(t, summary, "fragments : none",
		"a failed assembly must not be reported as the authoritative 'none'")
	assert.NotContains(t, summary, "commands  : none")
	assert.NotContains(t, summary, "skills    : none")
	assert.Contains(t, summary, "mcp       : none",
		"a genuinely empty set is still 'none' — that fact is real")
}

// A successful-but-empty listing keeps saying "none": that is a real, distinct
// fact and must not be converted into a scary unknown.
func TestNamesOrCount_EmptySuccessStillNone(t *testing.T) {
	assert.Equal(t, "none", namesOrCount(listed(nil), "ctxloom widget list"))
	assert.Contains(t, namesOrCount(listingFailed(assert.AnError), "ctxloom widget list"), "unavailable")
}
