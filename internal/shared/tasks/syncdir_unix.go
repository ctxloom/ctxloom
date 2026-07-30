//go:build !windows

package tasks

import "os"

// syncDir flushes dir's own entry list to stable storage. fsync on a FILE
// makes its CONTENTS durable but says nothing about the directory entry that
// names it, so a log file created and fsynced during a power loss can come
// back with its data intact and no name pointing at it — i.e. the event the
// caller was told had landed is gone. append() therefore syncs the parent
// directory when, and only when, it just created the log: every later append
// writes into an entry that is already durable, so the cost is one extra
// fsync in the life of a store.
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
