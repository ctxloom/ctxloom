//go:build windows

package coord

// fsyncDir is a no-op on Windows: a directory cannot be opened for the
// synchronization a Unix directory fsync performs, so there is nothing to
// call. Rename durability on NTFS is the filesystem's own concern here.
func fsyncDir(string) error { return nil }
