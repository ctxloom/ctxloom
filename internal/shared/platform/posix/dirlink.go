// Package posix holds the behaviour the POSIX platform packages (linux,
// darwin) share, so neither carries a copy of the other's.
package posix

import (
	"fmt"
	"os"
	"path/filepath"
)

// Linker makes directory links as symbolic links that name their target
// RELATIVELY: the same two trees are bind-mounted side by side into a
// container at different absolute paths, and only a relative link resolves
// on both sides.
type Linker struct{}

// LinkDir makes link a symbolic link to the directory target, named relative
// to link's parent. Both are absolute host paths; link's parent must exist and
// link must not.
func (Linker) LinkDir(link, target string) error {
	rel, err := filepath.Rel(filepath.Dir(link), target)
	if err != nil {
		return fmt.Errorf("link %s: %w", link, err)
	}
	return os.Symlink(rel, link)
}

// LinksTo reports whether link is the link LinkDir makes to target. Nothing at
// link is an error satisfying fs.ErrNotExist; anything else there — a real
// directory, a link elsewhere — is false.
func (Linker) LinksTo(link, target string) (bool, error) {
	if _, err := os.Lstat(link); err != nil {
		return false, err
	}
	got, err := os.Readlink(link)
	if err != nil {
		return false, nil
	}
	want, err := filepath.Rel(filepath.Dir(link), target)
	if err != nil {
		return false, fmt.Errorf("link %s: %w", link, err)
	}
	return got == want, nil
}

// UnlinkDir removes the symbolic link at link; unlink(2) never follows it.
func (Linker) UnlinkDir(link string) error { return os.Remove(link) }

// LinkTarget is the directory the symbolic link at link names, resolved
// against link's parent when it is relative.
func (Linker) LinkTarget(link string) (string, error) {
	got, err := os.Readlink(link)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(got) {
		got = filepath.Join(filepath.Dir(link), got)
	}
	return filepath.Clean(got), nil
}

// LinksResolveInContainers is true: a relative symbolic link inside a bind
// mount is followed by a Linux container's kernel.
func (Linker) LinksResolveInContainers() bool { return true }
