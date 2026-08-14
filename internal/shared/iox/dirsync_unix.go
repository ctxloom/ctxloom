//go:build !windows

package iox

import "os"

// syncDir flushes dir's own entry list to stable storage. An fsync on a FILE
// makes its CONTENTS durable but says nothing about the directory entry that
// names it, so a rename that lands right before a power loss can come back
// with the new file's data intact and the old name still in the directory —
// i.e. the guarantee Durable() callers asked for is gone. This is the same
// shim already duplicated as tasks/syncdir_unix.go and
// coord/artifactstore_dirsync.go; those two are left in place rather than
// forced onto this one (see the collapse unit's report for which of the two
// hand-rolled dir-syncs actually fold onto Durable() and which cannot).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
