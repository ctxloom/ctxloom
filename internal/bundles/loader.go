package bundles

import (
	"fmt"
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
)

// ContentGate is the per-item trust gate (trust rework, TR5) for resolved
// fragment/prompt content. It receives an item's full ref
// ("<bundle>#<kind>/<name>"), the effective-content hash of the EXACT bytes
// about to be exposed (pre-mustache), and the form ("raw"|"distilled"), and
// reports whether the item may be exposed (true) or must be withheld (false).
//
// A nil gate means no enforcement — management/listing loaders resolve content
// without gating so they can still see pending items (to review, accept, or
// stamp them). Fail-closed semantics are the gate's own responsibility: a
// resolve/hash/store error must return false (withhold), never default-allow.
type ContentGate func(ref, contentHash, form string) bool

// Loader loads bundles from disk (and an optional in-memory seed), caching
// parsed results for the lifetime of the loader.
type Loader struct {
	searchDirs      []string
	preferDistilled bool
	fs              afero.Fs
	mu              sync.RWMutex       // Protects cache
	cache           map[string]*Bundle // Cache of loaded bundles by path
	seeded          map[string]*Bundle // Canonical-ref → already-parsed bundle, populated from a remote source (e.g. BundleReader). Looked up before fs search.

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

// NewLoader creates a bundle loader.
// The loader caches loaded bundles in memory to avoid redundant disk reads.
func NewLoader(searchDirs []string, preferDistilled bool, opts ...LoaderOption) *Loader {
	l := &Loader{
		searchDirs:      searchDirs,
		preferDistilled: preferDistilled,
		fs:              afero.NewOsFs(),
		cache:           make(map[string]*Bundle),
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
// content. The hash MUST be the effective-content hash of the exact bytes about
// to be exposed (pre-mustache), so the gate keys on what the agent would see.
// source is the bundle's honest source ref (Bundle.contentSourceRef): canonical
// for a cloned bundle so its text gates like an executable, the local name for a
// project bundle so its text auto-trusts — the SAME keying the exec gate uses.
func (l *Loader) gateContent(source, kindDir, itemName, contentHash string, form ContentForm) bool {
	if l.gate == nil {
		return true
	}
	ref := source + "#" + kindDir + "/" + itemName
	if l.gate(ref, contentHash, string(form)) {
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
		path = filepath.Join(dir, osName, "bundle.yaml")
		if _, err := l.fs.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%w: %s", errs.ErrBundleNotFound, name)
}

// LoadFile reads a bundle from a specific path.
// Results are cached to avoid redundant disk reads when the same bundle
// is referenced multiple times (e.g., by multiple profiles).
// This method is safe for concurrent use.
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
	bundle.Name = extractBundleName(path)

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
			SkillCount:    b.SkillCount(),
			MCPCount:      b.MCPCount(),
			ProfileCount:  b.ProfileCount(),
		})
		seen.Add(name)
	}

	// Search bundle directories recursively
	for _, dir := range l.searchDirs {
		exists, err := afero.DirExists(l.fs, dir)
		if err != nil || !exists {
			continue
		}

		_ = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip directories we can't read
			}
			if info.IsDir() {
				// Check for bundle.yaml in directories
				bundlePath := filepath.Join(path, "bundle.yaml")
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
			if strings.HasSuffix(name, ".yaml") && name != "bundle.yaml" {
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
		})
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
	SkillCount    int
	MCPCount      int
	ProfileCount  int
	// Deleted marks a bundle that existed in an installed remote's history but
	// is gone from that repo at the current revision — removed upstream. Such an
	// entry carries only Name (the canonical ref); metadata is unavailable since
	// the content no longer exists to read.
	Deleted bool
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
		SkillCount:    bundle.SkillCount(),
		MCPCount:      bundle.MCPCount(),
		ProfileCount:  bundle.ProfileCount(),
	}, nil
}
