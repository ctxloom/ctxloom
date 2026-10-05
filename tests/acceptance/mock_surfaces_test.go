//go:build acceptance

package acceptance

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestMockSurfacesInSession: the step's predicate passes only a record that
// names every surface under the session store, and refuses one placed in the
// project, one missing, and an empty record — the shapes a released project
// delivery or a run that never launched would leave.
func TestMockSurfacesInSession(t *testing.T) {
	project, sessions := filepath.FromSlash("/w/project"), filepath.FromSlash("/w/home/.ctxloom/sessions")
	record := func(override map[string]string, drop string) string {
		var b strings.Builder
		for _, k := range mock.RecordSurfaceKeys() {
			if k == drop {
				continue
			}
			p := filepath.Join(sessions, "harp", "home", "mock", k)
			if o, ok := override[k]; ok {
				p = o
			}
			b.WriteString(k + "=" + p + "\n")
		}
		return b.String()
	}

	_, err := mockSurfacesInSession(record(nil, ""), sessions)
	require.NoError(t, err)

	for name, rec := range map[string]string{
		"a surface in the project": record(map[string]string{mock.RecordSettingsFile: filepath.Join(project, ".mock", "settings.json")}, ""),
		"a surface outside both":   record(map[string]string{mock.RecordMCPFile: filepath.FromSlash("/elsewhere/mcp.json")}, ""),
		"a surface not named":      record(nil, mock.RecordHooksFile),
		"an empty record":          "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mockSurfacesInSession(rec, sessions)
			require.Error(t, err)
		})
	}
}
