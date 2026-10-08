package engines

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestContextFile_EveryApproachHonoursOrRefusesANamedFile (materialize test
// 18): with ContextInputs.File set, every registered engine's context
// approach, at every root it offers, either writes the context at that path
// (Presented names it) or refuses with engine.ErrContextFileUnsupported. It
// never writes its own default file while ignoring File. Names no engine.
func TestContextFile_EveryApproachHonoursOrRefusesANamedFile(t *testing.T) {
	const rel = "docs/AGENTS.md"
	cell := present.Paths{SessionHome: present.Root{Host: "/home", Engine: "/home"}, ProjectRoot: present.Root{Host: "/p", Engine: "/p"}}
	start := present.New(present.OnHost(cell))
	reg := Registry()
	for _, name := range reg.NamesWhere(func(engine.Name, engine.Engine) bool { return true }) {
		kind, _ := reg.Lookup(name)
		a, ok := kind.Root().Surfaces()[present.Context]
		if !ok {
			continue
		}
		ctx, ok := a.(engine.ContextApproach)
		require.True(t, ok, "%s: the context approach is a ContextApproach", name)
		for _, root := range a.Traits().Roots {
			t.Run(string(name)+"/"+root.String(), func(t *testing.T) {
				fs := afero.NewMemMapFs()
				d, err := ctx.DeliverContext(start, root, engine.ContextInputs{Text: []byte("CTX"), File: rel}, fs)
				if err != nil {
					require.True(t, errors.Is(err, engine.ErrContextFileUnsupported), "a refusal is ErrContextFileUnsupported, got %v", err)
					return
				}
				base := cell.ProjectRoot.Host
				if root == present.RootSessionHome {
					base = cell.SessionHome.Host
				}
				want := filepath.Join(base, filepath.FromSlash(rel))
				require.Equal(t, want, d.Presented.HostPath, "Presented names the named file")
				for path := range d.Claims {
					require.Equal(t, want, path, "claims only the named file")
				}
				for _, path := range d.Files {
					require.Equal(t, want, path, "writes only the named file")
				}
			})
		}
	}
}
