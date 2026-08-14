//go:build windows

package iox

// syncDir is a no-op on Windows: a directory cannot be opened as a file and
// has no fsync equivalent there, so the durability Durable() buys on the
// unix build is simply not available. Returning nil is the honest answer —
// there is no operation to fail — and the guarantee is documented as
// unix-only (see Durable's doc) rather than silently claimed everywhere.
func syncDir(string) error { return nil }
