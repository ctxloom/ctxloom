package bundles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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

	// layouts is the set of FORMAT roots to search beneath each dir, most
	// preferred first. EMPTY means the dirs are searched as given — the
	// UNLAYERED case, which is the embedded builtin FS: its bundles sit at the
	// root of the embed, there is no format tree there to migrate, and
	// expanding it into per-format roots would search two directories that do
	// not exist and find no builtin bundle at all.
	layouts []paths.BundleLayout

	// failed records, per resolution name, WHY a bundle this reader should
	// have had is missing. Without it an unparseable bundle reaches the person
	// who asked for it by name as a bare "not found", which points them at
	// their spelling instead of at the file that will not parse.
	mu     sync.Mutex
	failed map[string]error
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
		layouts:    bundleLayoutPrecedence,
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

// ProjectAuthoredRead states the trust facts of content that IS in this
// project's own tree but did not come out of a Reader, because it is not a
// bundle: a `.ctxloom/profiles/<name>.yaml` profile's directly-declared hooks
// and MCP servers.
//
// It reports exactly what NewProjectReader reports for any file it finds in the
// project tree — ProvenanceProject, TrustCtxLocal, and no signature — so it
// asserts nothing that reader would not.
//
// IT IS THE ONE EXPORTED CONSTRUCTOR THAT SETS THE AXES, and that is a
// deliberate, narrow exception to the rule that provenance is never a caller's
// to choose. Two things hold it in place:
//
//   - It can only ever say PROJECT/LOCAL/UNSIGNED. There is no argument for
//     provenance, trust context or signature, so it cannot mint a builtin, a
//     companion, or a trusted signer, and it cannot claim a signature covers
//     anything.
//   - It is no wider than the claim it replaces: the same call site used to
//     assert locality implicitly, by handing the gate a bare-token ref, which
//     the ref grammar resolves to IsLocal — a claim of locality made in a
//     string, where nothing could see it. This makes the claim visible and
//     greppable; TestProjectAuthoredRead_CallSites pins the list.
//
// The principled fix is for profiles.ResolvedProfile to carry the read of the
// bundle (or project tree) it came from, which is a slice of its own.
func ProjectAuthoredRead(ref string, b *Bundle) BundleRead {
	return NewRead(ref, b, ProvenanceProject, TrustCtxLocal,
		SignatureFacts{Signature: SignatureNone, Signer: SignerNone})
}

// FS exposes the filesystem this reader read from, so a caller computing a
// skill's trust preimage from its on-disk tree uses the SAME filesystem the
// bundle was read through. Computing that preimage against a different fs
// produces a different hash for the same skill and silently withholds it.
func (r *localFSReader) FS() afero.Fs { return r.fsys }

// contentProvenance reports what this reader's filesystem HOLDS, so readersFS
// can pick the project tree over the embedded one without depending on the
// order the readers were composed in. It is deliberately not the exported
// Provenance of a read: this answers "whose filesystem is this", which is the
// only question the fs choice turns on.
func (r *localFSReader) contentProvenance() ProvenanceClass { return r.provenance }

// Read reports every bundle in every search directory.
//
// A directory that does not exist is ordinary — most search dirs are
// speculative. A directory that ERRORS, a path that cannot be walked, or a
// bundle file that will not parse are all faults and are reported through
// strictness (fatal-class in strict mode, warn-and-continue in degraded): an
// empty list with a nil error is how a permissions problem reaches the user as
// "your fragment does not exist".
//
// Each is reported ONCE per process, and the reason is structural: this walk
// runs once per generation, and a process may publish several (a reload after
// a pull, after a scaffold, per spawn). Every fault below is a property of the
// filesystem, so it cannot resolve itself between two generations inside one
// process, and reporting per generation turned a single malformed
// bundle into a screenful of identical lines that buried the one filename
// needing a fix. The FINDING still records per checkpoint window, so strict mode
// cannot be talked out of aborting by a repeat.
func (r *localFSReader) Read(ctx context.Context) ([]BundleRead, error) {
	var out []BundleRead
	seen := collections.NewSet[string]()
	// sightings records EVERY layout a name was found in, including the ones
	// `seen` shadowed. Without it a bundle present in both layouts is
	// indistinguishable from one present in a single layout, which is exactly
	// the fact Located.AlsoIn exists to report.
	sightings := map[string][]paths.BundleLayout{}
	for _, root := range r.searchRoots() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		exists, err := afero.DirExists(r.fsys, root.dir)
		if err != nil {
			strictness.FailOnce(strictness.ClassBundle, "check the permissions on your bundles directory",
				"cannot read bundles directory %s: %v", root.dir, err)
			continue
		}
		if !exists {
			continue
		}
		out = r.readDir(ctx, root, out, seen, sightings)
	}
	for i := range out {
		out[i].alsoIn = otherLayouts(sightings[out[i].ref], out[i].layout)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	return out, nil
}

// bundleSearchRoot is one directory to walk together with the layout it holds.
// The pair travels as one value because a read is worthless without it: a bare
// path cannot say which form answered, which is the silent wrong answer
// Catalog.Locate exists to remove.
type bundleSearchRoot struct {
	dir    string
	layout paths.BundleLayout
}

// bundleLayoutPrecedence is the order layouts are searched WITHIN one search
// directory, most preferred first.
//
// V2 WINS, and that is an INVERSION of what this reader used to do: a walk
// yields "<name>" before "<name>.yaml" only by lexical accident, and the
// bundle-as-tree design names the consequence of getting it wrong — a
// same-name migration keeps serving the monolith, so the migration "would
// appear to succeed and change nothing". Pinned by
// TestLocate_V2WinsOverV1AndReportsTheOther.
var bundleLayoutPrecedence = []paths.BundleLayout{paths.LayoutV2}

// searchRoots expands each configured search directory into its per-layout
// roots, most preferred first.
//
// The expansion is DIR-MAJOR: every layout of the first search directory is
// tried before any layout of the second. That keeps the search-path precedence
// this loader has always had — an earlier directory still wins outright — and
// makes the layout preference a tiebreak within one directory rather than a
// second axis competing with it.
func (r *localFSReader) searchRoots() []bundleSearchRoot {
	// No layouts: the dirs ARE the roots, and the reads they yield claim no
	// format. LayoutUnknown is the honest answer for content that sits outside
	// the format tree entirely, which is exactly what its own doc reserves it
	// for.
	if len(r.layouts) == 0 {
		roots := make([]bundleSearchRoot, 0, len(r.dirs))
		for _, dir := range r.dirs {
			roots = append(roots, bundleSearchRoot{dir: dir, layout: paths.LayoutUnknown})
		}
		return roots
	}
	roots := make([]bundleSearchRoot, 0, len(r.dirs)*len(r.layouts))
	for _, dir := range r.dirs {
		for _, l := range r.layouts {
			// Every format root is a SIBLING under the bundles directory, so
			// no walk can reach another format's tree and name its bundles
			// with a layout segment ("v2/unattended"). That is why the bundles
			// root itself is searched by nobody: it is the parent, not a root.
			roots = append(roots, bundleSearchRoot{dir: paths.BundlesLayoutRoot(dir, l), layout: l})
		}
	}
	return roots
}

// otherLayouts reduces every layout a name was seen in to the ones that did NOT
// answer, deduplicated and in precedence order.
func otherLayouts(seen []paths.BundleLayout, winner paths.BundleLayout) []paths.BundleLayout {
	var out []paths.BundleLayout
	for _, l := range bundleLayoutPrecedence {
		if l == winner {
			continue
		}
		for _, s := range seen {
			if s == l {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// readDir walks one search directory, appending a read per bundle found. Names
// already seen in an earlier directory win, which is the search-path precedence
// the loader has always had.
func (r *localFSReader) readDir(ctx context.Context, root bundleSearchRoot, out []BundleRead, seen collections.Set[string], sightings map[string][]paths.BundleLayout) []BundleRead {
	dir := root.dir
	walkErr := afero.Walk(r.fsys, dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Per-entry walk failure: report and keep walking, so one unreadable
			// subdirectory cannot hide every other bundle.
			strictness.FailOnce(strictness.ClassBundle, "check the permissions on your bundles directory",
				"skipping unreadable bundle path %s: %v", path, err)
			return nil
		}
		manifest, step := r.bundleAt(dir, path, info)
		if !step.IsBundle {
			return nil
		}
		// A DIRECTORY-form bundle owns everything beneath it: those files are its
		// ITEMS, not further bundles. Descending would read a tree's own
		// profiles/*.yaml or mcp/*.yaml back as malformed bundles — one spurious
		// failure per item file, on every command, which is how a warning channel
		// stops being read. Take the skip ONCE, up front, so the early returns
		// below cannot leak the walk back into the subtree.
		done := step.WalkSkip()
		name := step.Name
		sightings[name] = append(sightings[name], root.layout)
		if seen.Has(name) {
			return done
		}
		read, rerr := r.readBundle(ctx, manifest, name)
		if rerr != nil {
			r.recordFailure(name, rerr)
			// A local bundle that fails to load is fatal-class in strict mode
			// (fail-loudly); degraded mode keeps warn-and-skip so a corrupt
			// bundle never silently vanishes from a listing.
			strictness.FailOnce(strictness.ClassBundle, "fix or remove the bundle file",
				"skipping bundle %s: %v", manifest, rerr)
			return done
		}
		seen.Add(name)
		read.layout = root.layout
		out = append(out, read)
		return done
	})
	if walkErr != nil {
		// Walk itself gave up: the root could not be opened at all, and the
		// callback never ran for it.
		strictness.FailOnce(strictness.ClassBundle, "check the permissions on your bundles directory",
			"cannot walk bundles directory %s: %v", dir, walkErr)
	}
	return out
}

// recordFailure remembers why one bundle could not be read, for ReadFailure.
func (r *localFSReader) recordFailure(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed == nil {
		r.failed = map[string]error{}
	}
	r.failed[name] = err
}

// ReadFailures reports every bundle this reader's last read found but could not
// produce, keyed by resolution name. It is how "you asked for a bundle that
// will not parse" stays distinguishable from "you asked for a bundle that does
// not exist".
//
// The result is a COPY: a resolved set snapshots it, and handing out the live
// map would let a later read mutate what an earlier resolve reported.
func (r *localFSReader) ReadFailures() map[string]error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]error, len(r.failed))
	for name, err := range r.failed {
		out[name] = err
	}
	return out
}

// bundleAt reports the manifest to read for the bundle at one walked entry,
// together with paths.ClassifyBundleWalkEntry's decision about it — which
// carries both the entry's resolution name and whether the walk may descend
// below it. The classification is shared with every other bundles-root walk;
// see paths.BundleWalkStep for why the two answers travel together.
func (r *localFSReader) bundleAt(dir, path string, info os.FileInfo) (manifest string, step paths.BundleWalkStep) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return "", paths.BundleWalkStep{}
	}
	hasManifest := false
	if info.IsDir() {
		if _, serr := r.fsys.Stat(paths.BundleManifestPath(path)); serr == nil {
			hasManifest = true
		}
	}
	step = paths.ClassifyBundleWalkEntry(rel, info.IsDir(), hasManifest)
	if !step.IsBundle {
		return "", step
	}
	if step.IsTree {
		return paths.BundleManifestPath(path), step
	}
	return path, step
}

// readBundle parses one bundle document and establishes its signature facts.
func (r *localFSReader) readBundle(ctx context.Context, path, name string) (BundleRead, error) {
	data, err := afero.ReadFile(r.fsys, path)
	if err != nil {
		return BundleRead{}, fmt.Errorf("failed to read bundle: %w", err)
	}
	bundle, err := ParseBundle(data)
	if err != nil {
		return BundleRead{}, fmt.Errorf("failed to parse bundle %s: %w", path, err)
	}
	// A TREE-form bundle keeps its items in files beside this envelope, so the
	// parse above yielded only the bundle-level metadata. Replacing the value
	// here — rather than branching around everything below — is what keeps ONE
	// answer for identity, provenance and signature facts regardless of which
	// form the bundle was authored in. Non-tree forms return nil and fall
	// through unchanged; see readLocalTreeForm for how the three are told apart.
	tree, treeBundle, terr := r.readLocalTreeForm(ctx, path, bundle)
	if terr != nil {
		return BundleRead{}, terr
	}
	if treeBundle != nil {
		bundle = treeBundle
	}
	bundle.Path = path
	// A DECLARED name wins. The path-derived leaf name ("go" for
	// lang/go/bundle.yaml) is only the FALLBACK for a bundle that declares no
	// identity of its own, so it may never overwrite what the document said.
	// The read's ref stays the path-relative name a listing resolves by
	// ("lang/go"): it differs from the leaf name for nested bundles and both
	// have callers, so neither may quietly become the other.
	if bundle.Name == "" {
		bundle.Name = ExtractBundleName(path)
	}

	// A builtin's content TRUST identity is stamped HERE, and it deliberately
	// DIVERGES from the resolution identity below. The two are different
	// questions:
	//
	//   RESOLUTION ref (BundleRead.ref, `name`) is the handle a profile or a
	//   user asks by. Source class is not part of it — a bundle is addressed
	//   by what it declares, not by where it sits (ProvenanceClass's own doc:
	//   provenance is "a LABEL ... never the axis a gate keys on").
	//
	//   TRUST ref (bundle.sourceRef, trust.BuiltinRef(name)) is ACTUAL
	//   LOCATION, and trust keys on actual location. Source class belongs
	//   here and only here.
	//
	// This must run BEFORE newRead, whose stamp is only-if-empty: with sourceRef
	// already set, the bare resolution ref cannot overwrite it. Were sourceRef
	// left unset, newRead would stamp the bare `name`, Bundle.contentSourceRef()
	// (the fragment/prompt/skill trust ref for the loader-resolved-by-ref route)
	// would yield it, and it would key IsLocal — a second trust identity for
	// the same item, so a rejection recorded against one route would not
	// withhold the other (crispy-scoop). The injection route in
	// config.ResolveBuiltinBundleFragments reads the same value through
	// BundleRead.SourceRef(), so both routes still resolve to ONE
	// Ref{IsBuiltin: true} and one store key — now structurally, rather than
	// because two independently-built strings happened to match.
	//
	// A project bundle's sourceRef is untouched (stays the zero BundleRef), so
	// newRead stamps its bare resolution name and its IsLocal auto-trust is
	// unchanged.
	if r.provenance == ProvenanceBuiltin {
		typed, err := trust.BuiltinRef(name)
		if err != nil {
			warnUnmintableSource(name, err)
		}
		bundle.sourceRef = typed
		bundle.sourceRefSet = true
	}

	// Skills are a PACKAGE (a directory tree: SKILL.md plus siblings), which a
	// single-file bundle has no filesystem room to hold alongside it. Fail loud
	// rather than let a skill entry resolve against a directory that does not
	// exist.
	if len(bundle.Skills) > 0 && filepath.Base(path) != DirectoryFormManifest {
		return BundleRead{}, fmt.Errorf("bundle %s: skills require a directory-form bundle (bundle.yaml + skills/<name>/), not a single-file bundle (%s)",
			bundle.Name, filepath.Base(path))
	}

	facts := r.signatureFactsFor(path, data)
	// A TREE's envelope sibling covers a document that declares no items, so on
	// its own it says nothing about the payload. The manifest is what covers the
	// item files, and it can only downgrade the answer above — see
	// treeIntegrityFacts.
	if tree != nil {
		facts = r.treeIntegrityFacts(ctx, tree, facts)
	}
	facts.stamp(bundle)
	// The RESOLUTION ref is the bare path-relative name for EVERY class this
	// reader serves, builtins included. A builtin once minted
	// "builtin:<name>" here to dodge a map-key collision with a project bundle
	// of the same name; that pushed the source class into identity, which is
	// the rule this reader is not allowed to break, and it leaked "builtin:"
	// into every listing that showed a ref. The collision is now settled where
	// collisions belong — in Catalog.Resolve, which shadows the builtin, keeps
	// the project's, and SAYS SO. Source qualification survives only on
	// bundle.sourceRef, the trust key, stamped above.
	return NewRead(name, bundle, r.provenance, TrustCtxLocal, facts), nil
}

// signatureFactsFor resolves the signature axes from the sibling `.sig`.
//
// A sidecar that EXISTS but cannot be READ is its own state and must not read
// as unsigned: we cannot show it covers these bytes, so it is exactly as
// unpublishable as a stale one, and just as silent without this.
func (r *localFSReader) signatureFactsFor(path string, data []byte) SignatureFacts {
	sig, err := afero.ReadFile(r.fsys, path+SigSuffix)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return SignatureFacts{Signature: SignatureNone, Signer: SignerNone}
	case err != nil:
		return SignatureFacts{
			Signature: SignatureInvalid,
			Signer:    SignerUntrusted,
			Detail:    fmt.Sprintf("it could not be read: %v", err),
		}
	}
	return readSignatureFacts(data, sig, r.trustRoot())
}

// trustRoot is the root signer identity resolves against, or nil when the
// caller supplied none — in which case no key is trusted and every signature
// reads as untrusted, which is the fail-toward-less-exposure direction.
func (r *localFSReader) trustRoot() signing.TrustRoot { return r.cfg.root }
