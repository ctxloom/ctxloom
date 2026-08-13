package bundles

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
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

// fsStore is the filesystem Store adapter. It reads through an embedded Loader
// and writes/deletes through that Loader's afero.Fs, so reads and writes share
// one filesystem (the old Bundle.Save wrote via os while the Loader read via
// afero — a latent split this closes).
type fsStore struct {
	*Loader
	fs afero.Fs
}

// NewFSStore returns a filesystem-backed bundle Store over dirs, reading
// through a project reader on fsys and writing back through that same fsys —
// so a store's reads and writes cannot drift onto two filesystems. Like
// NewLoader it carries no form preference: a store yields every form it holds
// and the caller names the one it wants at the point of the read.
func NewFSStore(fsys afero.Fs, dirs []string) Store {
	if fsys == nil {
		fsys = afero.NewOsFs()
	}
	return &fsStore{Loader: NewLoader(NewProjectReader(fsys, dirs)), fs: fsys}
}

// Save writes the bundle back to its Path (which the caller sets — to the
// resolved path on load, or the target path on create), creating parent dirs.
//
// Every bundle mutation lands here — `bundle edit`, `fragment add`, `bundle
// distill`, all of it — which makes this the one place that can hold the
// signature-envelope spec's §3.0 invariant: the signed artifact is the PAIR
// (content bytes, detached signature). Writing new bytes beside a signature
// made over the old ones breaks the pair, and a broken pair is not a harmless
// staleness — downstream it is indistinguishable from an attack. So Save
// invalidates a signature it has outdated, loudly (invalidateStaleSignature).
func (s *fsStore) Save(b *Bundle) error {
	if b.Path == "" {
		return fmt.Errorf("bundle has no path set")
	}
	data, err := yaml.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal bundle: %w", err)
	}
	if err := s.fs.MkdirAll(filepath.Dir(b.Path), 0o755); err != nil {
		return fmt.Errorf("create bundle dir: %w", err)
	}
	// No AllowEmpty: yaml.Marshal of a *Bundle never produces zero bytes (a
	// struct marshals to at least "{}\n"), so the default empty-over-existing
	// refusal is pure upside here — it turns a hypothetical marshal
	// regression into a loud write failure instead of a silently truncated
	// bundle file.
	if err := iox.WriteFileAtomicFs(s.fs, b.Path, data, 0o644); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	// The bytes on disk changed, so the loader's memoized read of them is now a
	// lie. Dropping it is what makes a save-then-read within one command see
	// what was just written.
	s.invalidate()
	return s.invalidateStaleSignature(b.Path, data)
}

// SigSuffix is the detached-signature sibling suffix (signature-envelope spec
// §4.2): the armored publisher signature for `foo.yaml` is `foo.yaml.sig`.
const SigSuffix = ".sig"

// invalidateStaleSignature enforces the pair invariant for the bytes just
// written to path: any sibling `.sig` must cover them, or it must not exist.
//
// The check is the signature's own cryptographic self-consistency (does this
// blob cover these bytes), not a trust decision — the author's key is not in
// their own trust root, so VerifyPublisher cannot answer here, and does not
// need to. A signature that still covers the bytes (an idempotent re-save)
// survives untouched, byte-for-byte: nothing is ever re-signed or
// re-serialized.
//
// A signature that no longer covers them is REMOVED, because the alternative is
// worse than useless. Left in place, it makes every consumer of the published
// bundle raise a hard tamper alarm — trusted key, non-matching bytes — and
// withhold the content entirely, pointing the user at an attack that never
// happened. Removing it costs the publisher a re-sign; leaving it costs them
// every user.
//
// The removal is never silent. A publisher who signed once must be told at the
// moment their signature stopped being true, not discover it from a stranger's
// bug report. Failing to remove it IS a hard error: we will not return success
// having left a broken pair on disk.
func (s *fsStore) invalidateStaleSignature(path string, data []byte) error {
	sigPath := path + SigSuffix
	sig, err := afero.ReadFile(s.fs, sigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // No signature at all: the common case, nothing to invalidate.
	}
	if err != nil {
		// A signature that EXISTS but cannot be read is not the same as none:
		// returning success here leaves exactly the broken pair this function
		// exists to prevent, and a broken pair reads downstream as an attack.
		return fmt.Errorf("bundle %s was written, but its sibling signature %s could not be read to check whether it still covers those bytes "+
			"(publishing a stale signature would make every consumer see tampering): %w", filepath.Base(path), filepath.Base(sigPath), err)
	}
	if signing.CoversBytes(data, sig, signing.NamespacePublish) == nil {
		return nil // Still covers these exact bytes — an unchanged save. Leave it alone.
	}
	if err := s.fs.Remove(sigPath); err != nil {
		return fmt.Errorf("bundle %s was written, but its now-stale signature %s could not be removed "+
			"(publishing it would make every consumer see tampering): %w", filepath.Base(path), filepath.Base(sigPath), err)
	}
	name := strings.TrimSuffix(filepath.Base(path), ".yaml")
	clidiag.Fwarn(s.warnOut, "ctxloom", "signature removed: %s changed, so %s no longer covers it — re-sign with `ctxloom sign %s`",
		filepath.Base(path), filepath.Base(sigPath), name)
	return nil
}

// Delete removes the file backing the named bundle.
func (s *fsStore) Delete(name string) error {
	path, err := s.Find(name)
	if err != nil {
		return err
	}
	if err := s.fs.Remove(path); err != nil {
		return err
	}
	s.invalidate()
	return nil
}
