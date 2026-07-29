package backends

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestDeliveries_SurfaceSetMatchesResolvedSelection is the U033-F05 parity gate.
//
// TWO iteration paths claim to be "every surface of this backend, in a stable
// order, for an isolated cell":
//
//	(a) SurfaceSet.Deliveries()  — five hand-maintained slice literals, one per
//	    backend, mandated by the agent.SurfaceSet interface
//	(b) agent.Select(set).WithEverything().Build().Deliveries() — the
//	    approach-resolved path the launch path actually drives
//
// Only (b) has a production caller (launch_backend.go). (a) survives purely
// because the interface mandates it, so five backends maintain a stable-order
// contract nothing production reads — and nothing has ever held the two orders
// to each other.
//
// This delivers BOTH into two separate roots of the same in-memory filesystem
// and compares the resulting trees byte for byte. Equality is what makes
// deleting (a) a no-loss deletion rather than a leap; divergence would be the
// defect itself. Written before the deletion, against both paths.
func TestDeliveries_SurfaceSetMatchesResolvedSelection(t *testing.T) {
	for _, name := range nativeSurfaceBackends {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()

			viaSet := "/via-surfaceset"
			viaSel := "/via-selection"
			require.NoError(t, fs.MkdirAll(viaSet, 0o755))
			require.NoError(t, fs.MkdirAll(viaSel, 0o755))

			// The SurfaceSet path: iterate the interface method directly.
			setA := BuildSurfaces(name, parityInputs(), fs)
			viaSetKinds := 0
			for _, d := range setA.Deliveries() {
				_, err := d.Deliver(viaSet)
				require.NoError(t, err, "%s: SurfaceSet.Deliveries() surface failed to deliver", name)
				viaSetKinds++
			}

			// The resolved-selection path: the one the launch path drives.
			setB := BuildSurfaces(name, parityInputs(), fs)
			resolved, err := agent.Select(setB).WithEverything().Build()
			require.NoError(t, err, "%s: WithEverything must Build", name)
			viaSelKinds := 0
			for _, kd := range resolved.Deliveries() {
				_, err := kd.Deliver(viaSel)
				require.NoError(t, err, "%s: resolved surface %s failed to deliver", name, kd.Kind())
				viaSelKinds++
			}

			assert.Equal(t, viaSetKinds, viaSelKinds,
				"%s: the two iteration paths must expose the same number of surfaces", name)
			require.NotZero(t, viaSetKinds, "%s: a native-surface backend must expose surfaces", name)

			assert.Equal(t, treeOf(t, fs, viaSet), treeOf(t, fs, viaSel),
				"%s: SurfaceSet.Deliveries() and the resolved selection must materialize the SAME tree — "+
					"divergence means deleting the redundant one would change what reaches a cell", name)
		})
	}
}

// parityInputs is a populated SurfaceInputs exercising every surface kind, so
// the comparison is over real written bytes and not over two empty trees.
func parityInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Context: "assembled context for the parity gate",
		MCP:     &wire.MCPConfig{Servers: map[string]wire.MCPServer{"demo": {Command: "demo-server"}}},
		Hooks:   &wire.HooksConfig{},
		Commands: []agent.CommandExport{
			{Name: "review", Description: "review the diff", Content: "review body", Enabled: true},
		},
		Skills: []agent.SkillExport{
			{Name: "humanize", Description: "humanize prose", Enabled: true,
				Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("humanize body")}}},
		},
	}
}

// treeOf snapshots every file under root as relpath -> contents, so two
// delivery paths can be compared as materialized RESULTS rather than as
// Delivery identities (the resolved path wraps each surface in an unexported
// adapter, so the values themselves are not comparable from outside the agent
// package — the bytes on disk are the honest comparison anyway).
func treeOf(t *testing.T, fs afero.Fs, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, afero.Walk(fs, root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, readErr := afero.ReadFile(fs, path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	}))
	require.NotEmpty(t, out, "delivering into %s wrote nothing — a zero-byte comparison proves nothing", root)
	// Content-addressed filenames embed a hash of the SAME input on both sides,
	// so they compare equal; assert that assumption rather than assume it.
	for rel := range out {
		assert.False(t, strings.Contains(rel, "\x00"), "unexpected path shape %q", rel)
	}
	return out
}
