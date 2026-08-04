package bundles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/resources"
)

// localFSReader reads bundle documents out of directories on a filesystem.
//
// It is ONE implementation serving two sources — the project's own bundle
// directories and the bundles compiled into the binary — because an embed.FS
// adapted through afero is a filesystem, and a second body would have been the
// same walk, the same parse and the same signature check free to drift from
// this one. The two differ only in what they hard-code: their provenance label
// and their filesystem. Neither is a constructor argument.
type localFSReader struct {
	fsys       afero.Fs
	dirs       []string
	provenance ProvenanceClass
	cfg        readerConfig
}

// NewProjectReader reads the bundles a project authored in its own content
// tree: ProvenanceProject, TrustCtxLocal.
//
// Provenance and trust context are hard-coded, not parameters. A
// NewLocalFSReader(fs, dirs, provenance) would let any caller mint
// builtin-labelled or project-labelled content out of a call site, which is a
// trust bypass that reviews as ordinary wiring.
//
// Local content is trusted by LOCALITY, so a signature on it never gates:
// the reader still establishes the signature facts, because they are what tells
// an author their `.sig` no longer covers their bytes (see Loader's handling of
// the local/invalid row), but under TrustCtxLocal they are diagnostics.
func NewProjectReader(fsys afero.Fs, dirs []string, opts ...ReaderOption) Reader {
	if fsys == nil {
		fsys = afero.NewOsFs()
	}
	return &localFSReader{
		fsys:       fsys,
		dirs:       dirs,
		provenance: ProvenanceProject,
		cfg:        newReaderConfig(opts),
	}
}

// NewBuiltinReader reads the bundles embedded in the ctxloom binary
// (resources/builtin_bundles): ProvenanceBuiltin, TrustCtxLocal.
//
// A builtin is deliberately UNSIGNED — signing bytes with a key embedded in the
// binary that verifies them is circular — so it reports none/none and is local
// by construction: it did not cross an intermediary, it was compiled in. It is
// still a READ like any other, which is what keeps a rejected builtin item
// rejectable: rejection sits above every exemption and is applied downstream,
// not by hiding the content here.
func NewBuiltinReader(opts ...ReaderOption) Reader {
	return &localFSReader{
		fsys:       afero.FromIOFS{FS: resources.BuiltinBundlesFS()},
		dirs:       []string{"."},
		provenance: ProvenanceBuiltin,
		cfg:        newReaderConfig(opts),
	}
}

// FS exposes the filesystem this reader read from, so a caller computing a
// skill's trust preimage from its on-disk tree uses the SAME filesystem the
// bundle was read through. Computing that preimage against a different fs
// produces a different hash for the same skill and silently withholds it.
func (r *localFSReader) FS() afero.Fs { return r.fsys }

// Read reports every bundle in every search directory.
//
// A directory that does not exist is ordinary — most search dirs are
// speculative. A directory that ERRORS, a path that cannot be walked, or a
// bundle file that will not parse are all faults and are reported through
// strictness (fatal-class in strict mode, warn-and-continue in degraded): an
// empty list with a nil error is how a permissions problem reaches the user as
// "your fragment does not exist".
func (r *localFSReader) Read(ctx context.Context) ([]BundleRead, error) {
	var out []BundleRead
	seen := collections.NewSet[string]()
	for _, dir := range r.dirs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		exists, err := afero.DirExists(r.fsys, dir)
		if err != nil {
			strictness.Fail(strictness.ClassBundle, "check the permissions on your bundles directory",
				"cannot read bundles directory %s: %v", dir, err)
			continue
		}
		if !exists {
			continue
		}
		out = r.readDir(dir, out, seen)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	return out, nil
}

// readDir walks one search directory, appending a read per bundle found. Names
// already seen in an earlier directory win, which is the search-path precedence
// the loader has always had.
func (r *localFSReader) readDir(dir string, out []BundleRead, seen collections.Set[string]) []BundleRead {
	walkErr := afero.Walk(r.fsys, dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Per-entry walk failure: report and keep walking, so one unreadable
			// subdirectory cannot hide every other bundle.
			strictness.Fail(strictness.ClassBundle, "check the permissions on your bundles directory",
				"skipping unreadable bundle path %s: %v", path, err)
			return nil
		}
		manifest, name, ok := r.bundleAt(dir, path, info)
		if !ok || seen.Has(name) {
			return nil
		}
		read, rerr := r.readBundle(manifest, name)
		if rerr != nil {
			// A local bundle that fails to load is fatal-class in strict mode
			// (fail-loudly); degraded mode keeps warn-and-skip so a corrupt
			// bundle never silently vanishes from a listing.
			strictness.Fail(strictness.ClassBundle, "fix or remove the bundle file",
				"skipping bundle %s: %v", manifest, rerr)
			return nil
		}
		seen.Add(name)
		out = append(out, read)
		return nil
	})
	if walkErr != nil {
		// Walk itself gave up: the root could not be opened at all, and the
		// callback never ran for it.
		strictness.Fail(strictness.ClassBundle, "check the permissions on your bundles directory",
			"cannot walk bundles directory %s: %v", dir, walkErr)
	}
	return out
}

// bundleAt reports the manifest path and resolution name of the bundle at one
// walked entry: a directory holding bundle.yaml, or a *.yaml file that is not
// itself a directory-form manifest.
func (r *localFSReader) bundleAt(dir, path string, info os.FileInfo) (manifest, name string, ok bool) {
	if info.IsDir() {
		manifest = filepath.Join(path, DirectoryFormManifest)
		if _, err := r.fsys.Stat(manifest); err != nil {
			return "", "", false
		}
		rel, _ := filepath.Rel(dir, path)
		return manifest, filepath.ToSlash(rel), true
	}
	base := info.Name()
	if !strings.HasSuffix(base, ".yaml") || base == DirectoryFormManifest {
		return "", "", false
	}
	rel, _ := filepath.Rel(dir, path)
	return path, strings.TrimSuffix(filepath.ToSlash(rel), ".yaml"), true
}

// readBundle parses one bundle document and establishes its signature facts.
func (r *localFSReader) readBundle(path, name string) (BundleRead, error) {
	data, err := afero.ReadFile(r.fsys, path)
	if err != nil {
		return BundleRead{}, fmt.Errorf("failed to read bundle: %w", err)
	}
	bundle, err := ParseBundle(data)
	if err != nil {
		return BundleRead{}, fmt.Errorf("failed to parse bundle %s: %w", path, err)
	}
	bundle.Path = path
	// Bundle.Name is the LEAF name a load has always carried ("go" for
	// lang/go/bundle.yaml); the read's ref is the path-relative name a listing
	// resolves by ("lang/go"). They differ for nested bundles and both have
	// callers, so neither may quietly become the other.
	bundle.Name = ExtractBundleName(path)

	// Skills are a PACKAGE (a directory tree: SKILL.md plus siblings), which a
	// single-file bundle has no filesystem room to hold alongside it. Fail loud
	// rather than let a skill entry resolve against a directory that does not
	// exist.
	if len(bundle.Skills) > 0 && filepath.Base(path) != DirectoryFormManifest {
		return BundleRead{}, fmt.Errorf("bundle %s: skills require a directory-form bundle (bundle.yaml + skills/<name>/), not a single-file bundle (%s)",
			bundle.Name, filepath.Base(path))
	}

	facts := r.signatureFactsFor(path, data)
	facts.stamp(bundle)
	return newRead(name, bundle, r.provenance, TrustCtxLocal, facts), nil
}

// signatureFactsFor resolves the signature axes from the sibling `.sig`.
//
// A sidecar that EXISTS but cannot be READ is its own state and must not read
// as unsigned: we cannot show it covers these bytes, so it is exactly as
// unpublishable as a stale one, and just as silent without this.
func (r *localFSReader) signatureFactsFor(path string, data []byte) signatureFacts {
	sig, err := afero.ReadFile(r.fsys, path+SigSuffix)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return signatureFacts{signature: SignatureNone, signer: SignerNone}
	case err != nil:
		return signatureFacts{
			signature: SignatureInvalid,
			signer:    SignerUntrusted,
			detail:    fmt.Sprintf("it could not be read: %v", err),
		}
	}
	return readSignatureFacts(data, sig, r.trustRoot())
}

// trustRoot is the root signer identity resolves against, or nil when the
// caller supplied none — in which case no key is trusted and every signature
// reads as untrusted, which is the fail-toward-less-exposure direction.
func (r *localFSReader) trustRoot() signing.TrustRoot { return r.cfg.root }
