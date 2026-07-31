package iox

import (
	"os"

	"github.com/spf13/afero"
)

// WriteFileAtomic writes data to path by creating a UNIQUE temp file in the same
// directory, then renaming it over path. The unique name (not a fixed
// "<path>.tmp") keeps a concurrent writer — even one that slipped past an advisory
// lock — from clobbering another writer's in-flight temp before the rename, so a
// reader never observes a half-written file or a truncated peer temp. The temp
// file is fsynced before the rename so a power loss cannot persist the rename
// ahead of the data. The parent directory is NOT fsynced afterwards, so the
// replacement is atomically VISIBLE but not durable: after a power loss the
// directory entry may still name the previous file. The parent directory must
// already exist. perm is applied to the final file.
//
// There is ONE algorithm; this is its OS-filesystem entry point. A second,
// hand-copied transcription of WriteFileAtomicFs's steps would share no code
// and have no compiler-enforced link, leaving the two free to drift apart
// silently. atomicwrite_parity_test.go drives both entry points through one
// table and fails on any divergence in bytes, mode, error behaviour, or temp
// cleanup.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return WriteFileAtomicFs(afero.NewOsFs(), path, data, perm)
}
