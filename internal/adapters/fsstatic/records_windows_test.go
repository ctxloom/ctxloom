//go:build windows

package fsstatic

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// On Windows owner-only is a DACL, not a mode. A records directory that
// already exists carrying the ACL it inherited from its parent is made
// owner-only by opening the store and by Prepare, and an ownership record
// saved into it inherits the protection.
func TestRecords_AnExistingDirAndItsRecordsAreOwnerOnly_ADACL(t *testing.T) {
	fs := afero.NewOsFs()
	dir := filepath.Join(t.TempDir(), "records")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	rec, err := NewRecords(fs, dir)
	require.NoError(t, err)
	fileperm.OwnerOnly(t, dir)
	require.NoError(t, rec.Prepare(context.Background()))
	fileperm.OwnerOnly(t, dir)

	target := filepath.Join(t.TempDir(), "settings.json")
	testsupport.WriteFileString(t, fs, target, "{}\n", 0o644)
	_, err = rec.Apply(context.Background(), fs, target, delivery.ProjectWriter, addKey("project", "p"))
	require.NoError(t, err)
	fileperm.OwnerOnly(t, rec.path(target))
}

// A directory writeThrough creates for an approach's own state (claude's undo
// record) is owner-only as an ACL, and the file landed in it inherits that.
func TestWriteThrough_CreatesAMissingDirectoryOwnerOnly_ADACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records", "x.hew-record.yaml")

	require.NoError(t, writeThrough(afero.NewOsFs(), path, []byte("x"), 0o600))

	fileperm.OwnerOnly(t, filepath.Dir(path))
	fileperm.OwnerOnly(t, path)
}
