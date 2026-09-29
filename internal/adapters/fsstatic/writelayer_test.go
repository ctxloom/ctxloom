package fsstatic

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestWriteLayer_NamesEveryFileTheOverlayPutContentUnder drives each route by
// which afero's CopyOnWriteFs puts a file into its layer — a direct write, a
// copy-up of a base file (here by a chmod alone), and a rename's destination
// — and pins that each is found by name, with a removed file and a rename's
// source dropped.
func TestWriteLayer_NamesEveryFileTheOverlayPutContentUnder(t *testing.T) {
	root := t.TempDir()
	at := func(name string) string { return filepath.Join(root, name) }
	base := afero.NewOsFs()
	testsupport.WriteFile(t, base, at("existing"), []byte("base"), 0o644)

	layer := &writeLayer{Fs: afero.NewMemMapFs(), names: map[string]struct{}{}}
	overlay := afero.NewCopyOnWriteFs(base, layer)

	testsupport.WriteFile(t, overlay, at("direct"), []byte("d"), 0o600)
	require.NoError(t, overlay.Chmod(at("existing"), 0o755))
	testsupport.WriteFile(t, overlay, at("tmp"), []byte("r"), 0o600)
	require.NoError(t, overlay.Rename(at("tmp"), at("renamed")))
	testsupport.WriteFile(t, overlay, at("gone"), []byte("g"), 0o600)
	require.NoError(t, overlay.Remove(at("gone")))

	require.Equal(t, []string{at("direct"), at("existing"), at("renamed")}, layer.files())
}
