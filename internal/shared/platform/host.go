package platform

// The platform BEHAVIOURS: one small interface per concern, each implemented
// by a package per OS (platform/linux, platform/darwin, platform/windows),
// composed into Host and selected for the build target in this package's
// per-OS files — the ONE place an OS is chosen. A consumer depends on the
// concern it uses, never on an OS, so feature code carries no build-tagged
// files and no runtime.GOOS branch for these.

// DirLinker makes and recognises a directory link: one directory standing in
// for another.
type DirLinker interface {
	// LinkDir makes link a directory link to target (both absolute host
	// paths). link's parent must exist; link must not.
	LinkDir(link, target string) error
	// LinksTo reports whether link is the link LinkDir makes to target.
	// Nothing at link is an error satisfying fs.ErrNotExist; anything else
	// there is false.
	LinksTo(link, target string) (bool, error)
	// UnlinkDir removes the link LinkDir made at link, and never anything
	// inside the directory it names.
	UnlinkDir(link string) error
	// LinksResolveInContainers reports whether a link LinkDir made still
	// resolves when link's tree and target's tree are bind-mounted side by
	// side into a Linux container.
	LinksResolveInContainers() bool
}

// UserDirs locates the human's own folders.
type UserDirs interface {
	// DocumentsDir is the folder the human keeps documents in, as this
	// platform defines it.
	DocumentsDir() (string, error)
}

// PrivateTmpfs locates a per-user, owner-only, memory-backed directory: where
// a secret can be written without reaching a disk.
type PrivateTmpfs interface {
	// PrivateTmpfs is that directory, read through getenv, and whether the
	// platform offers one.
	PrivateTmpfs(getenv func(string) string) (dir string, ok bool)
}

// Host is every platform behaviour the build target's OS supplies.
type Host interface {
	DirLinker
	UserDirs
	PrivateTmpfs
}

// Current is the build target's Host.
func Current() Host { return current }
