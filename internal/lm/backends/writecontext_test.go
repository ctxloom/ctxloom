package backends

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteContext_OptOutBackends pins the dispatch opt-out: a backend that
// does not implement agent.ContextWriter — because it has no settings writer at
// all (acp/mock) or a settings writer without a native context surface (codex)
// — returns an empty report and no error, writing nothing. This is the clean
// opt-out the materialize orchestrator relies on for non-context backends.
func TestWriteContext_OptOutBackends(t *testing.T) {
	for _, name := range []string{"codex", "acp", "mock"} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			report, err := WriteContext(name, "/project", "assembled context", WithSettingsFS(fs))
			require.NoError(t, err)
			assert.Empty(t, report.Wrote, "opt-out backend writes nothing")
			assert.Empty(t, report.Removed)
		})
	}
}
