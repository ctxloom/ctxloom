package bundles

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
)

// Store is the read+write port (ADR 0026): a backing store bundles persist
// to. The filesystem adapter is the one returned by NewFSStore. Operations
// depends on this interface, never on a concrete store, so storage can change
// without touching core logic.
type Store interface {
	Load(name string) (*Bundle, error)
	Save(b *Bundle) error
	Delete(name string) error
}

var _ Store = (*fsStore)(nil)

// fsStore is the filesystem Store adapter. It reads through a Loader and
// writes/deletes through that Loader's own afero.Fs, so reads and writes cannot
// drift onto two filesystems (the old Bundle.Save wrote via os while the Loader
// read via afero — a latent split this closes).
type fsStore struct {
	*Loader
	fs afero.Fs
	// own, when set, is the reader this store built for itself (NewFSStore):
	// a view with no generation to pin, so the store re-resolves it after its
	// own writes. A store over a shared generation (NewStore) has nil here
	// and leaves the next generation to its caller.
	own Reader
}

// NewStore returns a Store over an EXISTING loader: it reads that loader's
// resolved set and writes through the filesystem that loader read from.
//
// Taking the loader rather than building one is the point. A store that
// resolved its own sources held a SECOND view of the same bundles, so a write
// through the store and a read through the session's loader disagreed until
// something re-read by luck — two caches of one thing, reconciled by accident.
// A write through the store changes what the readers would see; the caller
// announces that by building the next generation (config.Owner.Reload) —
// this store never re-reads on its own.
//
// It also settles which filesystem a write lands on: loader.FS() is the one the
// content was READ from, so a caller that injected a filesystem no longer has
// its reads honoured and its writes sent to the OS.
func NewStore(loader *Loader) Store {
	return &fsStore{Loader: loader, fs: loader.FS()}
}

// NewFSStore returns a Store over its own project reader, for the callers that
// have no session generation to share (a standalone distill over an explicit
// dir). Its view has no generation to pin, so a write through it is visible
// to its own next read. Prefer NewStore wherever a generation exists.
func NewFSStore(fsys afero.Fs, dirs []string) Store {
	if fsys == nil {
		fsys = afero.NewOsFs()
	}
	own := NewProjectReader(fsys, dirs)
	return &fsStore{Loader: NewLoader(own), fs: fsys, own: own}
}

// republish re-resolves a self-owned view after a write; a shared
// generation is left to its caller.
func (s *fsStore) republish() {
	if s.own != nil {
		s.Loader = NewLoader(s.own).WithReporter(s.cat.rep.Sink).WithVersionResolver(s.versionResolver, s.versionRoot)
	}
}

// Load resolves a bundle this project AUTHORED, and only that.
//
// The narrowing is the store's central invariant: Save writes back to
// Bundle.Path, so a name that resolved to remote, builtin or companion content
// would let a write land on something this project does not own. Expressing it
// as a scope over the shared set — rather than by giving the store its own
// project-only reader — is what lets the store share one resolved view with
// everything else without widening what it may write.
func (s *fsStore) Load(name string) (*Bundle, error) {
	return s.Loader.Catalog().Scoped(ProvenanceProject).Load(name)
}

// Save writes the bundle back to the tree whose envelope is at its Path (which
// the caller sets — to the resolved path on load, or the target path on
// create), creating the tree when it does not exist yet.
//
// Every bundle mutation lands here — `bundle edit`, `fragment add`, `bundle
// distill`, all of it. A tree's signature is its SHA256SUMS manifest and the
// .sigs/ entry over it; writing new bytes under a signed manifest leaves the
// manifest stale, which the reader reports as an INVALID signature and the
// author is told to re-sign (StaleSignatureAdvice). Nothing here has a
// second signature shape to keep in step.
func (s *fsStore) Save(b *Bundle) error {
	if b.Path == "" {
		return fmt.Errorf("bundle has no path set")
	}
	if filepath.Base(b.Path) != DirectoryFormManifest {
		return fmt.Errorf("bundle path %s is not a tree envelope: a bundle is <name>/%s plus its item files", b.Path, DirectoryFormManifest)
	}
	if err := s.saveTree(context.Background(), b); err != nil {
		return err
	}
	s.republish()
	return nil
}

// Delete removes a bundle: its whole tree. Removing only the envelope would
// leave every item file behind in a directory nothing reads.
func (s *fsStore) Delete(name string) error {
	path, err := s.Find(name)
	if err != nil {
		return err
	}
	if err := s.fs.RemoveAll(filepath.Dir(path)); err != nil {
		return err
	}
	s.republish()
	return nil
}
