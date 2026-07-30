//go:build windows

package tasks

// syncDir is a no-op on Windows: a directory cannot be opened as a file and
// has no fsync equivalent there, so the durability the unix build gets for a
// newly created log file is simply not available. Returning nil is the honest
// answer — there is no operation to fail — and the guarantee is documented as
// unix-only rather than silently claimed everywhere.
func syncDir(string) error { return nil }
