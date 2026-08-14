package iox

import (
	"os"

	"github.com/spf13/afero"
)

// Option configures an atomic-write call — WriteFileAtomic, WriteFileAtomicFs,
// or NewAtomicFile's Commit. The zero value (no options) is every entry
// point's historical default behavior.
type Option func(*writeConfig)

type writeConfig struct {
	allowEmpty bool
	durable    bool
}

// AllowEmpty opts a write out of the empty-over-existing refusal guard (see
// WriteFileAtomicFs's doc). The one legitimate shape is a writer that has
// already decided, with its own narrower reasoning, that a zero-byte result
// is a meaningful outcome rather than a bug upstream — e.g. a countersign
// index whose last entry was just removed, or a gitignore file whose entire
// content was a now-retired rule.
func AllowEmpty() Option {
	return func(c *writeConfig) { c.allowEmpty = true }
}

// Durable additionally fsyncs the PARENT DIRECTORY after the rename lands,
// upgrading the guarantee every entry point otherwise offers from atomic
// VISIBILITY to visibility plus DIRENT durability: after a power loss, the
// directory entry is guaranteed to still name the new file, not revert to
// naming whatever was there before. This is still not full journaling — the
// new file's own bytes were already made durable by the pre-rename fsync
// every entry point performs regardless of this option; what Durable adds is
// only the missing half, the NAME pointing at those bytes, which is exactly
// the gap WriteFileAtomicFs's doc calls out as not covered by default.
//
// A directory fsync needs a real file descriptor for the directory itself.
// On a filesystem that is not OS-backed (a test double such as
// afero.MemMapFs) there is nothing to open, so Durable is a documented
// no-op there and MemMapFs-backed tests are unaffected by opting in. On
// Windows a directory cannot be opened for sync at all, so Durable is a
// documented no-op there too — see dirsync_windows.go.
//
// Costs one extra fsync per write. Reserve it for sites where the rename
// silently reverting to the old name after a crash would be worse than the
// extra cost — e.g. a human decision or a rotation-lineage record whose loss
// is unrecoverable, not a cache or a log where the file simply gets
// rewritten again.
func Durable() Option {
	return func(c *writeConfig) { c.durable = true }
}

// resolveOptions applies opts over the zero value and returns the result.
func resolveOptions(opts []Option) writeConfig {
	var c writeConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WriteFileAtomic writes data to path by creating a UNIQUE temp file in the same
// directory, then renaming it over path. The unique name (not a fixed
// "<path>.tmp") keeps a concurrent writer — even one that slipped past an advisory
// lock — from clobbering another writer's in-flight temp before the rename, so a
// reader never observes a half-written file or a truncated peer temp. The temp
// file is fsynced before the rename so a power loss cannot persist the rename
// ahead of the data. By default the parent directory is NOT fsynced
// afterwards, so the replacement is atomically VISIBLE but not durable: after
// a power loss the directory entry may still name the previous file. Pass
// Durable() to also fsync the parent directory and close that gap — see its
// doc for the guarantee it adds and where it degrades to a no-op. The parent
// directory must already exist.
//
// perm is applied to the final file EXACTLY, via an explicit Chmod on the temp
// file — the process umask does not narrow it. This deliberately differs from
// os.WriteFile/afero.WriteFile, which pass perm to the create call where the
// kernel masks it, so a caller migrating from os.WriteFile under a restrictive
// umask gets a wider mode here than it used to. Pass the mode you actually
// want; do not rely on the umask to tighten it.
//
// There is ONE algorithm; this is its OS-filesystem entry point. A second,
// hand-copied transcription of WriteFileAtomicFs's steps would share no code
// and have no compiler-enforced link, leaving the two free to drift apart
// silently. atomicwrite_parity_test.go drives both entry points through one
// table and fails on any divergence in bytes, mode, error behaviour, or temp
// cleanup.
//
// Zero-length data over an EXISTING file is refused by default — see
// WriteFileAtomicFs's doc for the guard and AllowEmpty's escape hatch. Zero
// length data to a path with nothing there yet proceeds; a brand-new empty
// file is not a truncation.
func WriteFileAtomic(path string, data []byte, perm os.FileMode, opts ...Option) error {
	return WriteFileAtomicFs(afero.NewOsFs(), path, data, perm, opts...)
}
