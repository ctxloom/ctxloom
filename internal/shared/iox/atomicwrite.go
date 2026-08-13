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
// ahead of the data. The parent directory is NOT fsynced afterwards, so the
// replacement is atomically VISIBLE but not durable: after a power loss the
// directory entry may still name the previous file. The parent directory must
// already exist.
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
