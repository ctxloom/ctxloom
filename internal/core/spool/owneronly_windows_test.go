//go:build windows

package spool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// On Windows owner-only is a DACL, not a mode. A spool root that already
// exists carrying the ACL it inherited from its parent is made owner-only by
// the first writer, and what is created beneath it — the layout, a published
// message, an armed wake — inherits that protection.
func TestSpool_TheRootAndWhatIsBeneathItAreOwnerOnly_ADACL(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	root, err := Root(m, testHarp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))

	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: "x\n"})
	require.NoError(t, err)
	nonce, err := ArmWake(m, testHarp)
	require.NoError(t, err)

	delivered, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: "y\n"})
	require.NoError(t, err)
	require.NoError(t, Deliver(m, delivered, "m-perm", time.Now()))

	msg, err := m.Resolve(ref)
	require.NoError(t, err)
	fileperm.OwnerOnly(t, root)
	fileperm.OwnerOnly(t, filepath.Join(root, filepath.FromSlash(string(DirOutConsumed))))
	fileperm.OwnerOnly(t, filepath.Join(root, filepath.FromSlash(deliveredDirName), "m-perm"))
	fileperm.OwnerOnly(t, msg)
	fileperm.OwnerOnly(t, filepath.Join(root, filepath.FromSlash(wakeDirName), nonce))
}
