package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestDeliveries_ResolvedSelectionMaterializesEverySurface guards the ONE
// iteration path over a backend's surfaces.
//
// agent.SurfaceSet deliberately mandates no raw, unresolved Deliveries(): that
// is a second path, hand-maintained as a slice literal by four backends and
// called by no production code, for a materialized tree measured to be
// byte-identical to the one the launch path produces through
// agent.Select(set).WithEverything().Build().Deliveries().
//
// What survives here is the half that still has a subject: every native-surface
// backend must resolve WithEverything into one delivery per ADVERTISED kind,
// deliver each cleanly into an isolated root, and leave a non-empty tree behind.
// A backend that silently resolves to nothing is exactly the silent-no-op shape
// (exit 0, success, zero bytes) the raw path's absence must not reintroduce.
func TestDeliveries_ResolvedSelectionMaterializesEverySurface(t *testing.T) {
	for _, name := range nativeSurfaceBackends {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			root := "/cell"
			require.NoError(t, fs.MkdirAll(root, 0o755))

			set := BuildSurfaces(name, parityInputs(), fs)

			// One delivery per kind the backend actually advertises.
			wantKinds := 0
			for _, kind := range allSurfaceKinds {
				if _, ok := set.DefaultApproach(kind); ok {
					wantKinds++
				}
			}
			require.NotZero(t, wantKinds, "%s: a native-surface backend must advertise at least one kind", name)

			resolved, err := agent.Select(set).WithEverything().Build()
			require.NoError(t, err, "%s: WithEverything must Build", name)

			deliveries := resolved.Deliveries()
			assert.Len(t, deliveries, wantKinds,
				"%s: WithEverything must resolve one delivery per advertised kind", name)

			for _, kd := range deliveries {
				_, err := kd.Deliver(root)
				require.NoError(t, err, "%s: %s failed to deliver", name, kd.Kind())
			}

			assert.NotEmpty(t, treeOf(t, fs, root),
				"%s: delivering every resolved surface wrote ZERO files", name)
		})
	}
}

// parityInputs is a populated SurfaceInputs exercising every surface kind, so
// the assertions are over real written bytes and not over an empty tree.
func parityInputs() agent.SurfaceInputs {
	return agent.SurfaceInputs{
		Context:   "assembled context for the delivery gate",
		BundleMCP: map[string]wire.MCPServer{"demo": {Command: "demo-server"}},
		Hooks:     &wire.HooksConfig{},
		Commands: []agent.CommandExport{
			{Name: "review", Description: "review the diff", Content: "review body", Enabled: true},
		},
		Skills: []agent.SkillExport{
			{Name: "humanize", Description: "humanize prose", Enabled: true,
				Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("humanize body")}}},
		},
	}
}

// treeOf snapshots every file under root as relpath -> contents.
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
	return out
}
