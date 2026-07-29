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
// Without a shared recipe, mcpSurface and settingsSurface each carry TWO
// near-identical delivery bodies: Deliver(dir) and DeliverIsolated(). Four
// bodies whose only real differences are where `dir` comes from and whether the
// resulting path is recorded — so the delivery recipe (which writer, which
// receiver fields get threaded onto it, which arguments) has to be edited twice
// per surface, with nothing holding the two halves to each other.
// MCPCommandOverride is the exact shape of the hazard: it is threaded onto the
// writer in BOTH mcpSurface bodies and, per DeliverIsolated's own comment,
// deliberately in NEITHER settings body.
//
// This pins the invariant the shared recipe preserves: for a given surface, the
// well-known write and the isolated write produce the SAME bytes, and only the
// isolated one records a Path.
func TestDeliverAndDeliverIsolated_WriteIdenticalBytes(t *testing.T) {
	const (
		wellKnownDir = "/well-known"
		isolatedDir  = "/isolated"
	)

	newSurfaces := func(t *testing.T, fs afero.Fs) Surfaces {
		t.Helper()
		in := sampleInputs()
		in.MCPCommandOverride = "/usr/local/bin/ctxloom"
		return NewSurfaces(in, dirPlacement{dir: isolatedDir}, fs)
	}

	cases := []struct {
		name     string
		deliver  func(Surfaces) (agent.Delivered, error)
		isolated func(Surfaces) (agent.Delivered, error)
		path     func(Surfaces) string
		relPath  string
	}{
		{
			name:     "mcp",
			deliver:  func(s Surfaces) (agent.Delivered, error) { return s.MCP.Deliver(wellKnownDir) },
			isolated: func(s Surfaces) (agent.Delivered, error) { return s.MCP.DeliverIsolated() },
			path:     func(s Surfaces) string { return s.MCP.Path() },
			relPath:  ".mcp.json",
		},
		{
			name:     "settings",
			deliver:  func(s Surfaces) (agent.Delivered, error) { return s.Settings.Deliver(wellKnownDir) },
			isolated: func(s Surfaces) (agent.Delivered, error) { return s.Settings.DeliverIsolated() },
			path:     func(s Surfaces) string { return s.Settings.Path() },
			relPath:  filepath.Join(".claude", "settings.json"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			s := newSurfaces(t, fs)

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
			isolated, err := afero.ReadFile(fs, filepath.Join(isolatedDir, tc.relPath))
			require.NoError(t, err, "the isolated write must land at %s", tc.relPath)

			require.NotEmpty(t, wellKnown, "a zero-byte comparison proves nothing")
			assert.Equal(t, string(wellKnown), string(isolated),
				"%s: the two delivery bodies must apply the SAME recipe — differing bytes mean one body "+
					"gained a step the other did not", tc.name)

			assert.Equal(t, filepath.Join(isolatedDir, tc.relPath), tc.path(s),
				"DeliverIsolated must record the out-of-cwd path for the launch flag")
		})
	}
}
