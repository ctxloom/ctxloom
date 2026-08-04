package bundles

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// ContentGate is the per-item trust gate for resolved fragment/prompt content.
// It receives:
//
//   - ref     — the item's full ref, "<bundle>#<kind>/<name>"
//   - payload — the EXACT bytes about to be exposed (pre-mustache)
//   - form    — the LAYOUT form: "raw" | "distilled"
//   - signer  — the bundle's VERIFIED publisher identity, or "" for unsigned
//
// form is the layout axis ONLY. A countersignature binds a COMPOSITE
// attestation form that also names the item's role ("fragment/raw",
// "exec/hook", …), and that value is derived below the gate from the ref's kind
// — never passed in here. A gate call site therefore cannot name its own role,
// which is what stops a text item's approval from ever satisfying an
// executable's gate over identical bytes: the caller does not get a say.
//
// and reports whether the item may be exposed (true) or must be withheld
// (false).
//
// It takes BYTES, not a content hash, and that is the point: a hash can only be
// compared against a recorded hash, and a recorded hash is a file anything can
// write. Bytes can be VERIFIED against a signature. Handing the gate a
// precomputed hash would make the fast path "is this hash in the store?" — which
// is exactly the forgeable-file weakness the signature design exists to remove
// (spec §9.3, trap #2). The hash still exists, as an index; it just stopped
// being the authority.
//
// A nil gate means no enforcement — management/listing loaders resolve content
// without gating so they can still see pending items (to review, accept, or
// stamp them). Fail-closed semantics are the gate's own responsibility: a
// resolve/store error must return false (withhold), never default-allow.
type ContentGate func(ref string, payload []byte, form, signer string) bool

// Loader loads bundles from disk (and an optional in-memory seed), caching
// parsed results for the lifetime of the loader.
type Loader struct {
	searchDirs []string
	fs         afero.Fs
	mu         sync.RWMutex       // Protects cache
	cache      map[string]*Bundle // Cache of loaded bundles by path
	seeded     map[string]*Bundle // Canonical-ref → already-parsed bundle, populated from a remote source (e.g. BundleReader). Looked up before fs search.

	gate       ContentGate         // nil = no enforcement; set on exposure loaders only
	withheldMu sync.Mutex          // Protects withheld
	withheld   map[string]struct{} // refs the gate withheld this loader's lifetime

	// versionResolver materializes a specific historical commit-version of a
	// bundle (multi-version coexistence, trust rework, TR5). nil = no per-version
	// capability; only the version-aware methods (GetFragmentAtVersion /
	// ResolveFragmentVersions) consult it — Load and the default path never do, so
	// the lockfile-pinned default is unchanged.
	versionResolver BundleVersionResolver
	versionMu       sync.Mutex         // Protects versionCache
	versionCache    map[string]*Bundle // canonical-ref+"@"+commit → parsed historical bundle

	// warnOut is where user-facing diagnostics go (os.Stderr by default; see
	// WithWarnWriter). fsStore, which embeds this Loader, writes its
	// signature-invalidation notice here.
	warnOut io.Writer
}

// BundleVersionResolver materializes a specific historical commit-version of a
// bundle, identified by its version-less CANONICAL ref ("<url>@bundles/<path>")
// and an opaque commit revision, returning the parsed bundle as it existed at
// that commit. It backs the loader's per-version resolution: production wires it
// to the remote FetchItem primitive (remote.FetchRefBytes over the local clone
// cache); tests inject a fake. A non-nil error withholds that version
// (fail-closed) — the loader never falls back to a different version on failure.
type BundleVersionResolver func(canonicalRef, commit string) (*Bundle, error)

// seededPathPrefix is the sentinel that marks BundleInfo.Path entries whose
// content lives only in Loader.seeded. LoadFile uses the prefix to short-
// circuit straight back to the seeded bundle without touching the fs.
const seededPathPrefix = "<seeded>:"

// LoaderOption is a functional option for configuring a Loader.
type LoaderOption func(*Loader)

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) {
		l.fs = fs
	}
}

// WithSeededBundles pre-populates the loader with parsed bundles indexed by
// name. Seeded entries win over fs hits with the same name, which lets
// remote-pinned bundles served from a git clone cache (see operations.
// BundleReader) shadow any stale extracted copy left over from a previous
// install. Each call merges its map into any prior seed.
func WithSeededBundles(seeded map[string]*Bundle) LoaderOption {
	return func(l *Loader) {
		if l.seeded == nil {
			l.seeded = make(map[string]*Bundle, len(seeded))
		}
		// The seed key is the bundle's resolution identity; a bundle that
		// doesn't carry its own name would compose broken item names
		// ("/<item>"), so backfill from the key. The key is also the bundle's
		// canonical (cloned) source ref, recorded so the content gate keys by it
		// (honest local-vs-clone locality) instead of the short bundle.Name.
		for name, b := range seeded {
			if b == nil {
				continue
			}
			if b.Name == "" {
				b.Name = name
			}
			b.sourceRef = name
		}
		maps.Copy(l.seeded, seeded)
	}
}

// WithTrustGate attaches the per-item trust gate (trust rework, TR5) so this
// loader withholds fragment/prompt content the trust cascade denies. Only
// exposure loaders (assembly, ctxloom:// resources, fragment-reading hooks,
// SessionStart regen) set it; management/listing loaders leave it nil.
func WithTrustGate(gate ContentGate) LoaderOption {
	return func(l *Loader) {
		l.gate = gate
	}
}

// WithVersionResolver attaches the per-commit-version resolver (multi-version
// coexistence, trust rework, TR5) so the loader can materialize a specific
// historical version of a bundle via the version-aware methods. A nil resolver
// (the default) leaves the loader version-unaware: the lockfile-pinned default
// is the only version, and a pinned-version request fails closed.
func WithVersionResolver(resolver BundleVersionResolver) LoaderOption {
	return func(l *Loader) {
		l.versionResolver = resolver
	}
}

// WithWarnWriter redirects this loader/store's user-facing diagnostics (the
// clidiag "ctxloom: warning:" lines) away from stderr, so tests can read what
// the user would have been told. The default is os.Stderr: a warning nobody
// sees is the bug these diagnostics exist to prevent.
//
// ALL of them: the signature a Save invalidated, an unresolved bundle ref, and
// an ambiguous bare fragment ask. The last two dedup process-wide (see
// bundleWarner) but still emit through this writer — dedup is the process's
// business, the sink is the caller's.
func WithWarnWriter(w io.Writer) LoaderOption {
	return func(l *Loader) {
		l.warnOut = w
	}
}

// NewLoader creates a bundle loader.
// The loader caches loaded bundles in memory to avoid redundant disk reads.
//
// It carries NO form preference. Raw-vs-distilled is a PROCESS-stage decision
// (docs/design/engine-delivery-seam.design.md, "ALL processing lives in the
// middle"): the read stage yields whichever form its caller asks for, per call,
// so one loader serves two consumers that want different forms and the read
// stage holds no policy of its own.
func NewLoader(searchDirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{
		searchDirs: searchDirs,
		fs:         afero.NewOsFs(),
		cache:      make(map[string]*Bundle),
		warnOut:    os.Stderr,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// gateContent runs the trust gate (if any) for a resolved fragment/prompt.
// Returns true to expose. A nil gate (management/listing loaders) always
// exposes. A withheld item is recorded — deduplicated by ref — so an assembly
// caller can surface the count ("N withheld") via Withheld without leaking
// content.
//
// payload MUST be the exact bytes about to be exposed (pre-mustache), so the
// gate decides on what the agent would actually see; profile-variable
// substitution happens after and cannot smuggle content past the gate.
// source is the bundle's honest source ref (Bundle.contentSourceRef): canonical
// for a cloned bundle so its text gates like an executable, the local name for a
// project bundle so its text auto-trusts — the SAME keying the exec gate uses.
// signer is the bundle's verified publisher identity, carried straight through.
func (l *Loader) gateContent(source, kindDir, itemName string, payload []byte, form ContentForm, signer string) bool {
	if l.gate == nil {
		return true
	}
	ref := source + "#" + kindDir + "/" + itemName
	if l.gate(ref, payload, string(form), signer) {
		return true
	}
	l.withheldMu.Lock()
	if l.withheld == nil {
		l.withheld = make(map[string]struct{})
	}
	l.withheld[ref] = struct{}{}
	l.withheldMu.Unlock()
	return false
}

// Gate returns the loader's attached trust gate (nil when none is set — a
// management/listing loader). Lets a caller that must gate OTHER items through
// the IDENTICAL decision (e.g. builtin bundle fragments, which bypass the
// loader's own content choke since they never resolve through Load) share this
// loader's gate rather than building and opening a redundant one.
func (l *Loader) Gate() ContentGate {
	return l.gate
}

// FS returns the filesystem this loader reads bundle trees from. A skill's
// trust preimage is derived from its on-disk tree (BundleSkill.ContentPayload),
// so a caller computing that preimage for an item this loader resolved MUST
// use this same filesystem — computing it against a different fs would produce
// a different hash for the same skill and silently withhold it.
func (l *Loader) FS() afero.Fs {
	return l.fs
}

// Withheld returns the item refs the trust gate withheld over this loader's
// lifetime, deduplicated and sorted. Empty when no gate is set or nothing was
// withheld. Callers surface the count so the user knows content was hidden;
// returning the refs (not their content) keeps the disclosure content-free.
func (l *Loader) Withheld() []string {
	l.withheldMu.Lock()
	defer l.withheldMu.Unlock()
	if len(l.withheld) == 0 {
		return nil
	}
	out := make([]string, 0, len(l.withheld))
	for ref := range l.withheld {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// seededPath builds the synthetic path used in BundleInfo for seeded bundles.
// LoadFile inverts the encoding to recover the name.
func seededPath(name string) string { return seededPathPrefix + name }

// seededNameFromPath returns ("name", true) when path is a sentinel produced
// by seededPath. Returns ("", false) for real fs paths.
func seededNameFromPath(path string) (string, bool) {
	if rest, ok := strings.CutPrefix(path, seededPathPrefix); ok {
		return rest, true
	}
	return "", false
}

// Load reads a bundle by name.
// Name can be:
//   - Simple name: "go-tools" -> searches for go-tools.yaml or go-tools/bundle.yaml
//   - Remote-qualified: "alice/go-tools" -> searches in alice/ subdirectory
//   - Local canonical: "ctxloom:local@bundles/go-tools" -> same fs search as the
//     simple name; this is the qualified identity the assembly pipeline carries
//     (see remote.CanonicalBundleRef).
//
// Seeded bundles (see WithSeededBundles) win over fs hits with the same
// name; this is how remote-pinned bundles delivered by operations.
// BundleReader shadow any stale extracted copy still on disk.
func (l *Loader) Load(name string) (*Bundle, error) {
	if b, ok := l.lookupSeeded(name); ok {
		return b, nil
	}
	if ref, err := remote.ParseReference(name); err == nil && ref.IsLocal && ref.ItemType == remote.ItemTypeBundle {
		name = ref.Path
	}
	path, err := l.Find(name)
	if err != nil {
		return nil, err
	}
	return l.LoadFile(path)
}

// lookupSeeded returns the seeded bundle for name, if any. Cheap read-only.
// Seeded bundles are keyed by their version-less canonical ref (the lockfile
// key shape), so a ref carrying a content version ("...@<sha>") is normalized
// to that form when the exact lookup misses.
func (l *Loader) lookupSeeded(name string) (*Bundle, bool) {
	if l.seeded == nil {
		return nil, false
	}
	if b, ok := l.seeded[name]; ok {
		return b, true
	}
	if key, ok := remote.CanonicalKey(name); ok && key != name {
		b, ok := l.seeded[key]
		return b, ok
	}
	return nil, false
}

// Find locates a bundle file by name (supports paths with slashes like "github.com/user/repo/bundle").
func (l *Loader) Find(name string) (string, error) {
	// Security: validate name
	if err := ValidateBundleName(name); err != nil {
		return "", err
	}

	// Convert forward slashes to OS-specific separator
	osName := filepath.FromSlash(name)

	for _, dir := range l.searchDirs {
		// Try direct path: name.yaml
		path := filepath.Join(dir, osName+".yaml")
		if _, err := l.fs.Stat(path); err == nil {
			return path, nil
		}

		// Try directory path: name/bundle.yaml
		path = filepath.Join(dir, osName, DirectoryFormManifest)
		if _, err := l.fs.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%w: %s", errs.ErrBundleNotFound, name)
}

// LoadFile reads a bundle from a specific path.
// Results are cached to avoid redundant disk reads when the same bundle
// is referenced multiple times (e.g., by multiple profiles).
//
// CONCURRENCY, precisely. The CALL is safe from any goroutine: the cache map
// is mutex-protected and a concurrent miss resolves to one shared entry. The
// RESULT is not a private copy — every caller asking for the same path gets
// the SAME *Bundle, and a seeded bundle is a pointer the seeder still holds.
// Bundle's item maps are exported and StampSigner mutates in place, so a
// caller that writes through the returned pointer races every other holder.
// Treat what LoadFile hands back as READ-ONLY; mutate a bundle before it is
// seeded or cached (which is what the signer-stamping load paths do), never
// after.
//
// Synthetic seeded-bundle paths (see seededPath) bypass the fs and return
// the corresponding seeded bundle. Real fs paths use the on-disk cache.
func (l *Loader) LoadFile(path string) (*Bundle, error) {
	if name, ok := seededNameFromPath(path); ok {
		if b, ok := l.lookupSeeded(name); ok {
			return b, nil
		}
		return nil, fmt.Errorf("seeded bundle %q not found", name)
	}

	// Check cache first (read lock)
	l.mu.RLock()
	if cached, ok := l.cache[path]; ok {
		l.mu.RUnlock()
		return cached, nil
	}
	l.mu.RUnlock()

	// Load from disk (no lock held during I/O)
	data, err := afero.ReadFile(l.fs, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}

	bundle, err := ParseBundle(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse bundle %s: %w", path, err)
	}

	bundle.Path = path
	bundle.Name = ExtractBundleName(path)

	// Skills are a PACKAGE (a directory tree: SKILL.md + optional sibling
	// files), which a single-file bundle ("<name>.yaml") has no filesystem
	// room to hold alongside it. Fail loud here rather than let a skill entry
	// silently resolve against a directory that doesn't exist.
	if len(bundle.Skills) > 0 && filepath.Base(path) != DirectoryFormManifest {
		return nil, fmt.Errorf("bundle %s: skills require a directory-form bundle (bundle.yaml + skills/<name>/), not a single-file bundle (%s)", bundle.Name, filepath.Base(path))
	}

	l.checkLocalSignature(path, bundle.Name, data)

	// Cache for future loads (write lock)
	l.mu.Lock()
	// Double-check in case another goroutine cached it while we were loading
	if cached, ok := l.cache[path]; ok {
		l.mu.Unlock()
		return cached, nil
	}
	l.cache[path] = bundle
	l.mu.Unlock()

	return bundle, nil
}

// checkLocalSignature reports — and only reports — a sibling `.sig` that does
// not cover the bundle bytes just read from path.
//
// IT IS NOT A GATE, and deliberately cannot become one: it returns nothing, and
// LoadFile calls it for its side effect alone. A bundle reached through this
// function is project-authored local content in the tree's own bundles
// directory (every bundles.Loader is built over cfg.GetBundleDirs() /
// paths.LocalBundlesPath — remote content never resolves here, it is seeded
// pre-parsed by config.loadRemoteBundleSeed with its publisher signature already
// verified at fetch). Local content is trusted by LOCALITY, at
// operations.EffectiveTrust's local step, above every signature step. A
// signature on it is metadata for the day it is promoted, never permission to
// read it.
//
// So why say anything? Because the metadata has exactly one consumer —
// PROMOTION — and a stale sidecar is worthless to it in every direction:
// `bundle move` (and ExportBundle beneath it) verifies the pair before bytes
// leave the machine and REFUSES a stale one, since publishing a trusted key
// over non-matching bytes hands every consumer a tamper alarm for an attack
// that never happened; `bundle push` never reads the sidecar and simply
// publishes the bytes unsigned. Either way the signature does not travel, and
// without this line the author finds that out at publish time, long after the
// edit that caused it.
//
// Cheap by construction: it runs only on a cache MISS (LoadFile caches per
// path), and the overwhelmingly common case — no sidecar at all — costs one
// failed open and returns silently.
func (l *Loader) checkLocalSignature(path, name string, data []byte) {
	sig, err := afero.ReadFile(l.fs, path+SigSuffix)
	if errors.Is(err, fs.ErrNotExist) {
		return // Unsigned: the common case, and nothing to say about it.
	}
	if err != nil {
		// A sidecar that EXISTS but cannot be read is not the same as none: we
		// cannot show it covers these bytes, so it is exactly as unpublishable
		// as a stale one, and just as silent without this.
		l.warnStaleSignature(path, name, fmt.Sprintf("it could not be read: %v", err))
		return
	}
	if verr := signing.CoversBytes(data, sig, signing.NamespacePublish); verr != nil {
		l.warnStaleSignature(path, name, verr.Error())
	}
}

// List returns all available bundles. Seeded bundles are listed first so
// that when an fs walk turns up a stale extracted copy with the same name,
// the seeded entry stays authoritative.
func (l *Loader) List() ([]*BundleInfo, error) {
	var bundles []*BundleInfo
	seen := collections.NewSet[string]()

	// Seeded bundles take precedence — emit them with a sentinel path that
	// LoadFile knows how to short-circuit.
	for name, b := range l.seeded {
		bundles = append(bundles, &BundleInfo{
			Name:          name,
			Path:          seededPath(name),
			Version:       b.Version,
			Description:   b.Description,
			Tags:          b.Tags,
			FragmentCount: b.FragmentCount(),
			CommandCount:  b.CommandCount(),
			MCPCount:      b.MCPCount(),
			ProfileCount:  b.ProfileCount(),
			Signer:        b.Signer(),
		})
		seen.Add(name)
	}

	// Search bundle directories recursively
	for _, dir := range l.searchDirs {
		// "Does not exist" and "cannot be read" are different facts and must
		// not share an exit. A configured bundles root that is
		// absent is ordinary — most searchDirs are speculative — but one that
		// errors on stat is a fault, and folding it into the same `continue`
		// produced an empty list with a nil error, so every downstream sweep
		// then told the user their fragment did not exist when the truth was
		// that their bundles directory could not be read.
		exists, err := afero.DirExists(l.fs, dir)
		if err != nil {
			strictness.Fail(strictness.ClassBundle, "check the permissions on your bundles directory",
				"cannot read bundles directory %s: %v", dir, err)
			continue
		}
		if !exists {
			continue
		}

		if werr := afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// Per-entry walk failure: report and keep walking, so one
				// unreadable subdirectory cannot hide every other bundle —
				// the same warn-and-continue shape the per-bundle failure
				// paths below use, but no longer SILENT.
				strictness.Fail(strictness.ClassBundle, "check the permissions on your bundles directory",
					"skipping unreadable bundle path %s: %v", path, err)
				return nil
			}
			if info.IsDir() {
				// Check for bundle.yaml in directories
				bundlePath := filepath.Join(path, DirectoryFormManifest)
				if _, err := l.fs.Stat(bundlePath); err == nil {
					relPath, _ := filepath.Rel(dir, path)
					bundleName := filepath.ToSlash(relPath)
					if seen.Has(bundleName) {
						return nil
					}
					bundleInfo, err := l.loadBundleInfo(bundlePath, bundleName)
					if err == nil {
						bundles = append(bundles, bundleInfo)
						seen.Add(bundleName)
					} else {
						// A local bundle that fails to load is fatal-class in
						// strict mode (fail-loudly): the warning streams either
						// way, and a startup choke owner aborts on the finding.
						// Degraded mode keeps the warn-and-skip so a corrupt
						// bundle never silently vanishes from list output.
						strictness.Fail(strictness.ClassBundle, "fix or remove the bundle file",
							"skipping bundle %s: %v", bundlePath, err)
					}
				}
				return nil
			}

			name := info.Name()
			// Check for .yaml files (bundle files)
			if strings.HasSuffix(name, ".yaml") && name != DirectoryFormManifest {
				relPath, _ := filepath.Rel(dir, path)
				bundleName := strings.TrimSuffix(filepath.ToSlash(relPath), ".yaml")
				if seen.Has(bundleName) {
					return nil
				}
				bundleInfo, err := l.loadBundleInfo(path, bundleName)
				if err == nil {
					bundles = append(bundles, bundleInfo)
					seen.Add(bundleName)
				} else {
					strictness.Fail(strictness.ClassBundle, "fix or remove the bundle file",
						"skipping bundle %s: %v", path, err)
				}
			}
			return nil
		}); werr != nil {
			// Walk itself gave up (the root could not be opened at all — the
			// callback never even runs for that case). Previously `_`-assigned.
			strictness.Fail(strictness.ClassBundle, "check the permissions on your bundles directory",
				"cannot walk bundles directory %s: %v", dir, werr)
		}
	}

	// Sort by name
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].Name < bundles[j].Name
	})

	return bundles, nil
}

// BundleInfo holds metadata about a bundle without loading full content.
type BundleInfo struct {
	Name          string
	Path          string
	Version       string
	Description   string
	Tags          []string
	FragmentCount int
	CommandCount  int
	MCPCount      int
	ProfileCount  int
	// Deleted marks a bundle that existed in an installed remote's history but
	// is gone from that repo at the current revision — removed upstream. Such an
	// entry carries only Name (the canonical ref); metadata is unavailable since
	// the content no longer exists to read.
	Deleted bool

	// Held marks a lockfile entry frozen at its recorded SHA (LockEntry.Pinned,
	// toggled by `ctxloom bundle hold`/`unhold`): `remote upgrade` leaves it put
	// even when its constraint would allow a newer commit.
	//
	// It is carried into the listing because a hold is a decision someone made
	// ON PURPOSE, and a hold nobody can see is indistinguishable from a broken
	// pull. Without it, "we froze this deliberately" and "the sync is failing"
	// render as identical output, and the only way to tell them apart is diffing
	// lockfiles by hand.
	Held bool

	// Retracted marks a bundle the publisher WITHDREW (LockEntry.Retracted),
	// learned from the remote manifest at the last pull that had the network.
	// RetractedReason is the publisher's stated reason — display only, never a
	// decision input.
	//
	// Silence about this is worse than silence about a hold: the content is
	// still installed and still being served while its publisher has said not to
	// use it.
	Retracted       bool
	RetractedReason string

	// Signer is the VERIFIED publisher identity of this bundle's bytes, or ""
	// when the bundle is unsigned — no signature, or one by a key this machine
	// does not trust to publish. It is Bundle.Signer() carried through, so it is
	// never an unverified claim and never comes from the bundle's own content.
	//
	// It is on the listing because "unsigned" is otherwise invisible: unsigned
	// remote content is withheld from exposure and does NOT appear in the
	// pending-review list (unsigned is not pending), so nothing named the bundle
	// or the reason. See doctorCheckContentTrust.
	Signer string
}

func (l *Loader) loadBundleInfo(path, name string) (*BundleInfo, error) {
	bundle, err := l.LoadFile(path)
	if err != nil {
		return nil, err
	}

	return &BundleInfo{
		Name:          name,
		Path:          path,
		Version:       bundle.Version,
		Description:   bundle.Description,
		Tags:          bundle.Tags,
		FragmentCount: bundle.FragmentCount(),
		CommandCount:  bundle.CommandCount(),
		MCPCount:      bundle.MCPCount(),
		ProfileCount:  bundle.ProfileCount(),
	}, nil
}
