//go:build windows

package fsstatic

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// On Windows owner-only is a DACL, not a mode, and it is the established
// records root's (paths.EnsureHomeRoots establishes it with Private.Ensure):
// the store applies none of its own, and a claims record it saves there —
// through a temp file renamed into place — inherits the root's owner-only
// ACE.
func TestRecords_ARecordUnderAnEstablishedDirIsOwnerOnly_ADACL(t *testing.T) {
	fs := afero.NewOsFs()
	dir := filepath.Join(t.TempDir(), "records")
	require.NoError(t, safefs.New().Private.Ensure(dir))

	rec, err := NewRecords(fs, dir)
	require.NoError(t, err)
	target := filepath.Join(t.TempDir(), "settings.json")
	testsupport.WriteFileString(t, fs, target, "{}\n", 0o644)
	b := safefs.NewBatch(fs, func(_ string, fn func() error) error { return fn() })
	require.NoError(t, rec.In(b).Stage(target, delivery.ProjectWriter, []present.Claim{{Pointer: "/project", Value: "p"}}))
	_, err = b.Commit()
	require.NoError(t, err)
	fileperm.OwnerOnly(t, dir)
	fileperm.OwnerOnly(t, rec.path(target))
}
