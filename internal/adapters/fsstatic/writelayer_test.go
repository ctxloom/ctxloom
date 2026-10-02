package fsstatic

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestWriteLayer_NamesEveryFileTheOverlayPutContentUnder drives each route by
// which afero's CopyOnWriteFs puts a file into its layer — a file opened for
// writing in place, a copy-up of a base file (here by a chmod alone), and a
// rename's destination (every atomic write lands that way) — and pins that
// each is found by name, with a removed file and a rename's source dropped.
// A new file created through the overlay reaches the layer as an OpenFile
// alone, the one route no rename or copy-up also notes.
func TestWriteLayer_NamesEveryFileTheOverlayPutContentUnder(t *testing.T) {
	root := t.TempDir()
	at := func(name string) string { return filepath.Join(root, name) }
	base := afero.NewOsFs()
	testsupport.WriteFile(t, base, at("existing"), []byte("base"), 0o644)

	layer := &writeLayer{Fs: afero.NewMemMapFs(), names: map[string]struct{}{}}
	overlay := afero.NewCopyOnWriteFs(base, layer)

	testsupport.WriteFile(t, overlay, at("direct"), []byte("d"), 0o600)
	opened, err := iox.Create(overlay, at("opened"))
	require.NoError(t, err)
	require.NoError(t, opened.Close())
	require.NoError(t, overlay.Chmod(at("existing"), 0o755))
	testsupport.WriteFile(t, overlay, at("tmp"), []byte("r"), 0o600)
	require.NoError(t, safefs.Rename(overlay, at("tmp"), at("renamed")))
	testsupport.WriteFile(t, overlay, at("gone"), []byte("g"), 0o600)
	require.NoError(t, overlay.Remove(at("gone")))

	require.Equal(t, []string{at("direct"), at("existing"), at("opened"), at("renamed")}, layer.files())
}
