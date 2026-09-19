// Package realpath answers one question — is this path inside that root —
// with symlinks resolved on both sides.
//
// It is a LEAF, importing nothing but the standard library, and that is
// structural rather than tidy. Two callers need this fact and NEITHER may
// import the other: internal/core/paths owns the on-disk layout, while
// internal/shared/tasks/taskstest guards test isolation, and internal/core/paths's
// own tests reach taskstest through internal/testsupport. A helper living in
// either package puts an import cycle in the other's test binary. A leaf below
// both is the only home where the rule can be stated once instead of copied.
package realpath

import (
	"path/filepath"
	"strings"
)

// Under reports whether path is root itself or lives beneath it.
//
// BOTH sides are resolved, and that is load-bearing on macOS: the OS temp root
// is a symlink there and t.TempDir hands back the unresolved form, so comparing
// an unresolved root reports a properly isolated store as unsandboxed.
// Resolving an already-resolved path is a no-op, so a caller that resolved
// first loses nothing.
func Under(path, root string) bool {
	path, root = Resolve(path), Resolve(root)
	if path == "" || root == "" {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// Resolve returns p symlink-resolved and cleaned, falling back to the cleaned
// absolute form when the path does not exist — a nonexistent path still has to
// compare sensibly against a root.
func Resolve(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// The leaf may not exist; resolve the deepest ancestor that does, so a
	// /tmp symlink still compares correctly against the resolved temp root.
	dir, leaf := filepath.Split(filepath.Clean(abs))
	if dir == "" || filepath.Clean(dir) == filepath.Clean(abs) {
		return filepath.Clean(abs)
	}
	return filepath.Join(Resolve(filepath.Clean(dir)), leaf)
}
