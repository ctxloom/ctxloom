package claude

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestDeliverAndDeliverIsolated_WriteIdenticalBytes is the parity gate between
// a surface's two delivery entry points.
//
// Without a shared recipe, each surface carries TWO near-identical delivery
// bodies whose only real differences are where `dir` comes from and whether the
// resulting path is recorded — so the delivery recipe (which writer, which
// receiver fields get threaded onto it, which arguments) has to be edited twice
// per surface, with nothing holding the two halves to each other.
// A per-surface receiver field is the exact shape of the hazard: denyTools is
// threaded onto the writer in BOTH settings bodies and deliberately in NEITHER
// MCP body, so a recipe edited in only one body silently splits the two.
//
// MCP now reaches its two writes through two APPROACHES sharing one embedded
// mcpWriter, and settings still through one type's two forms. The invariant is
// the same either way, and is what this pins: the project-file write and the
// private write produce the SAME bytes, and only the private one records a
// Path for the launch flag.
func TestDeliverAndDeliverIsolated_WriteIdenticalBytes(t *testing.T) {
	const (
		wellKnownDir  = "/well-known"
		isolatedDir   = "/isolated"
		engineHomeDir = "/engine-home"
	)
	roots := runRoots(wellKnownDir, isolatedDir, engineHomeDir)

	build := func(t *testing.T, fs afero.Fs) builtSurfaces {
		t.Helper()
		in := sampleInputs()
		in.DenyTools = []string{"Task"}
		return newSurfaces(in, fs)
	}

	cases := []struct {
		name       string
		deliver    func(builtSurfaces) (agent.Delivered, error)
		isolated   func(builtSurfaces) (agent.Delivered, error)
		path       func(builtSurfaces) string
		privateDir string // the root the private write lands under
		relPath    string
	}{
		{
			name:       "mcp",
			deliver:    func(s builtSurfaces) (agent.Delivered, error) { return s.MCPUnsafe.Deliver(roots) },
			isolated:   func(s builtSurfaces) (agent.Delivered, error) { return s.MCP.Deliver(roots) },
			path:       func(s builtSurfaces) string { return s.MCP.Path() },
			privateDir: engineHomeDir,
			relPath:    ".mcp.json",
		},
		{
			name:       "settings",
			deliver:    func(s builtSurfaces) (agent.Delivered, error) { return s.Settings.Deliver(roots) },
			isolated:   func(s builtSurfaces) (agent.Delivered, error) { return s.Settings.DeliverIsolated(roots) },
			path:       func(s builtSurfaces) string { return s.Settings.Path() },
			privateDir: isolatedDir,
			relPath:    filepath.Join(".claude", "settings.json"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			s := build(t, fs)

			assert.Empty(t, tc.path(s), "Path() must be empty before any delivery")

			handle, err := tc.deliver(s)
			require.NoError(t, err)
			require.NotNil(t, handle)
			assert.Empty(t, tc.path(s),
				"the well-known write must NOT record a Path — only the out-of-cwd variant feeds a launch flag")

			isoHandle, err := tc.isolated(s)
			require.NoError(t, err)
			require.NotNil(t, isoHandle)

			wellKnown, err := afero.ReadFile(fs, filepath.Join(wellKnownDir, tc.relPath))
			require.NoError(t, err, "the well-known write must land at %s", tc.relPath)
			isolated, err := afero.ReadFile(fs, filepath.Join(tc.privateDir, tc.relPath))
			require.NoError(t, err, "the private write must land at %s beneath %s", tc.relPath, tc.privateDir)

			require.NotEmpty(t, wellKnown, "a zero-byte comparison proves nothing")
			assert.Equal(t, string(wellKnown), string(isolated),
				"%s: the two delivery bodies must apply the SAME recipe — differing bytes mean one body "+
					"gained a step the other did not", tc.name)

			assert.Equal(t, filepath.Join(tc.privateDir, tc.relPath), tc.path(s),
				"the private write must record its path for the launch flag")
		})
	}
}
