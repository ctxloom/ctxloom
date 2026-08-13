// Package archive is the packed-archive backend for the content access
// surface: a read-only content.Store over a zip or tar.gz carrying a bundle
// tree.
//
// # It does not implement extraction, and that is the point
//
// Extraction is where an archive backend goes wrong. An archive arrives from
// somewhere else by definition, so its entries are attacker-controlled: a
// "../../x" name, an absolute path, a symlink pointing out of the root, a
// device node, a decompression bomb. internal/bundles.HardenedExtract already
// refuses every one of those, normalizes modes to exactly 0755/0644 (preserving
// the load-bearing scripts/ exec bit while never honouring setuid/setgid/sticky
// or world-writable), and caps total bytes and entry count.
//
// This package therefore CALLS it rather than walking the archive itself. A
// second extractor would be a second place to get those checks right, and the
// one that got them wrong would be the one nobody was reading.
//
// # Why it lives outside internal/content
//
// It needs internal/bundles for the extractor, and internal/bundles is meant to
// consume the content package rather than the reverse. A leaf subpackage means
// that dependency cannot become a cycle.
//
// # Extract-then-read, rather than read-through
//
// The archive is expanded ONCE into an in-memory filesystem at construction and
// served from there. That is deliberate: the hardened extractor's guarantees
// are stated over a completed extraction, and streaming entries lazily would
// mean re-deriving them per read — the same second implementation this package
// exists to avoid. Archives are bounded by MaxTotalBytes, so the memory cost is
// bounded too.
package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// ErrEmptyArchive reports a store constructed with no archive bytes. It is a
// caller error rather than an empty store: "you handed me nothing" and "this
// archive contains nothing" are different answers and must not be conflated.
var ErrEmptyArchive = errors.New("content/archive: no archive bytes")

// Store is a read-only content.Store over one packed archive.
//
// It deliberately does NOT implement content.Writer. An archive is a fixed set
// of bytes; a Put here would mutate a temporary extraction that nothing reads
// back and return success — a silent no-op, which is exactly the failure this
// codebase has to keep designing out.
type Store struct {
	inner *content.TreeStore
}

var _ content.Store = (*Store)(nil)

// New extracts an archive into memory and serves it as a content.Store.
//
// The archive's single top-level directory is the BUNDLE, matching the
// skill_archive codec's own contract (HardenedExtract returns exactly one
// topDir), so a store built from one archive holds exactly one bundle.
//
// prov is the caller's, not this package's, to decide: the same archive bytes
// are a local export when you produced them and a remote artifact when someone
// sent them to you, and only the caller knows which. An invalid Provenance is
// refused by content.NewTreeStore rather than defaulted, so a store can never
// silently claim an origin it was not given.
func New(data []byte, prov content.Provenance) (*Store, error) {
	if len(data) == 0 {
		return nil, ErrEmptyArchive
	}
	format, err := bundles.DetectArchiveFormat(data)
	if err != nil {
		return nil, fmt.Errorf("content/archive: %w", err)
	}
	fsys := afero.NewMemMapFs()
	// HardenedExtract STRIPS the archive's single top-level directory: it
	// returns that directory's name and writes its CONTENTS directly into
	// destDir. A tree store, though, needs a root whose children are bundle
	// directories — so the archive is expanded into a staging dir and then
	// re-rooted under the stripped name, which is the bundle's identity.
	// Extracting straight into the final root would produce a store whose
	// "bundles" were fragments/, hooks/ and mcp/, which enumerates happily and
	// is entirely wrong.
	const stage = "/stage"
	const root = "/root"
	topDir, err := bundles.HardenedExtract(fsys, data, format, stage, bundles.ExtractOptions{})
	if err != nil {
		// Surfaced verbatim: the extractor's rejections name WHICH entry and
		// WHY, and a caller diagnosing a refused archive needs that, not a
		// generic "could not read archive".
		return nil, fmt.Errorf("content/archive: %w", err)
	}
	if topDir == "" {
		return nil, fmt.Errorf("content/archive: the archive has no top-level directory, so it names no bundle")
	}
	if err := reroot(fsys, stage, root+"/"+topDir); err != nil {
		return nil, err
	}
	inner, err := content.NewTreeStore(fsys, root, prov)
	if err != nil {
		return nil, err
	}
	return &Store{inner: inner}, nil
}

// reroot copies an extracted tree from src to dst, preserving each file's
// mode.
//
// It copies rather than renaming because the mode is the whole reason this
// backend is interesting: HardenedExtract has already normalized every file to
// exactly 0755 or 0644, and the skill script's exec bit is load-bearing all the
// way to the model. A re-root that dropped it would leave an archive backend
// that reads bytes correctly and ships an unrunnable script.
func reroot(fsys afero.Fs, src, dst string) error {
	return afero.Walk(fsys, src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return fsys.MkdirAll(target, 0o755)
		}
		body, rerr := afero.ReadFile(fsys, p)
		if rerr != nil {
			return fmt.Errorf("content/archive: re-root %q: %w", rel, rerr)
		}
		if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		// iox.WriteFileAtomicFs applies perm EXACTLY via its own Chmod (see its
		// doc), unlike afero.WriteFile which only sets mode at creation — the
		// manual re-Chmod this used to need is now redundant and dropped.
		// AllowEmpty: HardenedExtract already normalized every entry's mode and
		// capped its size; whether an entry is zero-length is a property of the
		// archive's content, not this function's business to second-guess.
		if err := iox.WriteFileAtomicFs(fsys, target, body, info.Mode().Perm(), iox.AllowEmpty()); err != nil {
			return fmt.Errorf("content/archive: re-root %q: %w", rel, err)
		}
		return nil
	})
}

func (s *Store) Bundles(ctx context.Context) ([]content.BundleID, error) {
	return s.inner.Bundles(ctx)
}

func (s *Store) Open(ctx context.Context, id content.BundleID) (content.Bundle, error) {
	return s.inner.Open(ctx, id)
}
