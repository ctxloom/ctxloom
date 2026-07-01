package operations

import (
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// getTrustStore returns the per-item trust store for cfg, or the injected store
// (for testing). Threads the configured afero fs so the store reads/writes the
// same filesystem as the rest of the operation.
func getTrustStore(cfg *config.Config, injected *trust.Store, fs afero.Fs) (*trust.Store, error) {
	if injected != nil {
		return injected, nil
	}
	baseDir := getBaseDir(cfg)
	return trust.New(paths.TrustPath(baseDir), trust.WithFS(getFS(fs)))
}

// EffectiveTrustRequest carries the inputs for the trust cascade. The caller
// supplies the item Ref and the content hash of the bytes actually being
// exposed (the resolver never fetches or hashes — that is the caller's job, in
// TR5). Store/Registry/FS are optional injection points for testing.
type EffectiveTrustRequest struct {
	Ref         trust.Ref
	ContentHash string

	Store    *trust.Store     `json:"-"`
	Registry *remote.Registry `json:"-"`
	FS       afero.Fs         `json:"-"`
}

// EffectiveTrustResult reports the cascade outcome and which tier decided it.
type EffectiveTrustResult struct {
	Decision trust.Decision `json:"decision"`
	Source   trust.Source   `json:"source"`
}

// Trusted reports whether the cascade allowed exposure. It is the boolean the
// TR3 list-JSON stamp surfaces as "trusted"; Source (as a plain string) is the
// companion "trust_source".
func (r EffectiveTrustResult) Trusted() bool {
	return r.Decision == trust.Allow
}

// EffectiveTrust is the sole owner of the per-item trust cascade. It unifies
// the per-item trust store with the inherited remote tier (remotes.yaml's
// TrustBundles) at read time, evaluating exactly:
//
//	DENY  if content_hash ∈ denylist                     (denylist)
//	DENY  if a sticky ref-level blacklist entry exists    (blacklist)
//	ALLOW if a grant exists for {repo, ref, content_hash} (explicit-grant)
//	else bundle posture, if set                           (bundle)
//	else local fragment|prompt → ALLOW                    (local)
//	else remote.TrustBundles ? ALLOW : DENY               (remote)
//	else DENY                                             (default)
//
// It is fail-closed: a corrupt/unreadable trust store or remote registry denies
// rather than degrading to allow-by-default, and any tier that cannot positively
// justify exposure falls through to the terminal default-DENY.
func EffectiveTrust(cfg *config.Config, req EffectiveTrustRequest) (*EffectiveTrustResult, error) {
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		// A trust store we cannot read may hold a blacklist/denylist we would
		// otherwise miss — deny everything rather than silently reopen it.
		clidiag.Warn("ctxloom", "trust store unreadable, denying all items: %v", err)
		return decide(trust.Deny, trust.SourceDefault), nil
	}

	repoURL := req.Ref.CanonicalURL()
	refKey := req.Ref.Key()
	hash := req.ContentHash

	// 1. Content-hash denylist — exact bad content, regardless of ref or repo.
	if store.DenylistMatch(hash) {
		return decide(trust.Deny, trust.SourceDenylist), nil
	}
	// 2. Sticky ref-level blacklist — deny always wins over grants/postures and
	//    survives content changes.
	if store.BlacklistMatch(repoURL, refKey) {
		return decide(trust.Deny, trust.SourceBlacklist), nil
	}
	// 3. Explicit per-item grant bound to this exact content hash.
	if _, ok := store.GrantMatch(repoURL, refKey, hash); ok {
		return decide(trust.Allow, trust.SourceExplicitGrant), nil
	}
	// 4. SHA-agnostic bundle posture.
	if dec, ok := store.BundlePosture(req.Ref.Bundle); ok {
		return decide(dec, trust.SourceBundle), nil
	}
	// 5. Project-authored LOCAL content is auto-allowed — every kind, including
	//    executables (config-level MCP, ctxloom:local, and project-bundle
	//    hooks/MCP). Locality is honest here: the gate and stamps key bundle items
	//    by their source ref (canonical for a cloned bundle → IsLocal false, so a
	//    clone falls through to the remote/bundle tiers). "You authored it in this
	//    project, so it is trusted; a clone is not."
	if req.Ref.IsLocal {
		return decide(trust.Allow, trust.SourceLocal), nil
	}
	// 6. Inherited remote.TrustBundles posture (SHA-agnostic).
	registry, rerr := effectiveTrustRegistry(cfg, req)
	if rerr != nil {
		clidiag.Warn("ctxloom", "trust: remote registry unreadable, denying: %v", rerr)
		return decide(trust.Deny, trust.SourceDefault), nil
	}
	if trusted, found := remoteTrusted(registry, repoURL); found {
		if trusted {
			return decide(trust.Allow, trust.SourceRemote), nil
		}
		return decide(trust.Deny, trust.SourceRemote), nil
	}
	// 7. Terminal fail-closed default.
	return decide(trust.Deny, trust.SourceDefault), nil
}

func decide(d trust.Decision, s trust.Source) *EffectiveTrustResult {
	return &EffectiveTrustResult{Decision: d, Source: s}
}

// effectiveTrustRegistry returns the remote registry for the remote tier,
// building it lazily so the earlier (store-only) tiers never touch the registry
// fs. Honors an injected registry for testing.
func effectiveTrustRegistry(cfg *config.Config, req EffectiveTrustRequest) (*remote.Registry, error) {
	if req.Registry != nil {
		return req.Registry, nil
	}
	return getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
}

// remoteTrusted reports whether a remote whose canonical URL matches canonicalURL
// is registered, and whether it carries TrustBundles. Both sides are
// canonicalized through the SAME function so a registered remote and the ref's
// repo URL cannot diverge on a spelling variant.
func remoteTrusted(reg *remote.Registry, canonicalURL string) (trusted bool, found bool) {
	if reg == nil || canonicalURL == "" {
		return false, false
	}
	for _, rem := range reg.List() {
		if trust.CanonicalRepoURL(rem.URL) == canonicalURL {
			return rem.TrustBundles, true
		}
	}
	return false, false
}

// --- Mutations (one-path, mirroring SetRemoteTrust; no CLI yet — TR2) ---------

// SetItemTrustRequest grants trust to the currently-resolved version of an item.
type SetItemTrustRequest struct {
	// Ref is the item reference, "<bundle-ref>#<kind>/<name>" where bundle-ref
	// is a canonical URL ref, a ctxloom:local ref, or a plain local bundle name,
	// kind is fragments|skills|mcp (legacy "prompts" still accepted). A trailing
	// "@<commit>" on the bundle ref is recorded as provenance.
	Ref string

	Store  *trust.Store    `json:"-"`
	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetItemTrustResult reports the recorded grant.
type SetItemTrustResult struct {
	Status      string `json:"status"` // "granted"
	Ref         string `json:"ref"`
	RepoURL     string `json:"repo_url"`
	ContentHash string `json:"content_hash"`
	Form        string `json:"form"`
}

// SetItemTrust records a per-item trust grant bound to the item's current
// effective-content hash (distilled or raw, per config), so a later content
// change drops the grant and forces re-review. The hash is always recomputed
// from the resolved content — never read from the author-supplied content_hash
// field.
func SetItemTrust(cfg *config.Config, req SetItemTrustRequest) (*SetItemTrustResult, error) {
	tRef, loadRef, version, err := parseTrustItemRef(req.Ref)
	if err != nil {
		return nil, err
	}
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}
	hash, form, err := computeItemHash(cfg, loader, tRef, loadRef)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %q to grant trust: %w", req.Ref, err)
	}
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		return nil, err
	}
	if err := store.AddGrant(tRef.CanonicalURL(), tRef.Key(), hash, string(form), version); err != nil {
		return nil, err
	}
	return &SetItemTrustResult{
		Status:      "granted",
		Ref:         tRef.Key(),
		RepoURL:     tRef.CanonicalURL(),
		ContentHash: hash,
		Form:        string(form),
	}, nil
}

// SetBlacklistRequest blacklists an item.
type SetBlacklistRequest struct {
	Ref string

	Store  *trust.Store    `json:"-"`
	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetBlacklistResult reports the recorded blacklist.
type SetBlacklistResult struct {
	Status      string `json:"status"` // "blacklisted"
	Ref         string `json:"ref"`
	RepoURL     string `json:"repo_url"`
	ContentHash string `json:"content_hash,omitempty"` // empty if the item could not be resolved
}

// SetBlacklist writes BOTH companion components of a blacklist: the sticky
// ref-level entry (denies the ref regardless of content/version, survives
// changes) AND the item's current content hash into the content denylist (so a
// renamed/moved identical copy stays blocked). The ref-level entry is written
// even when the item cannot be resolved (e.g. already deleted) — the content
// hash is then simply omitted from the denylist.
func SetBlacklist(cfg *config.Config, req SetBlacklistRequest) (*SetBlacklistResult, error) {
	tRef, loadRef, _, err := parseTrustItemRef(req.Ref)
	if err != nil {
		return nil, err
	}
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}
	// Best-effort hash: blacklisting must not fail just because the content is
	// gone — the sticky ref entry is the durable guarantee.
	hash, _, herr := computeItemHash(cfg, loader, tRef, loadRef)
	if herr != nil {
		hash = ""
	}
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		return nil, err
	}
	if err := store.Blacklist(tRef.CanonicalURL(), tRef.Key(), hash); err != nil {
		return nil, err
	}
	return &SetBlacklistResult{
		Status:      "blacklisted",
		Ref:         tRef.Key(),
		RepoURL:     tRef.CanonicalURL(),
		ContentHash: hash,
	}, nil
}

// SetBundleTrustRequest sets a SHA-agnostic posture for a whole bundle.
type SetBundleTrustRequest struct {
	Bundle string
	Trust  bool

	Store *trust.Store `json:"-"`
	FS    afero.Fs     `json:"-"`
}

// SetBundleTrustResult reports the recorded bundle posture.
type SetBundleTrustResult struct {
	Status string `json:"status"` // "trusted" | "untrusted"
	Bundle string `json:"bundle"`
}

// SetBundleTrust records a per-bundle posture that cascades to grant-less items.
func SetBundleTrust(cfg *config.Config, req SetBundleTrustRequest) (*SetBundleTrustResult, error) {
	if req.Bundle == "" {
		return nil, fmt.Errorf("bundle is required")
	}
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		return nil, err
	}
	dec := trust.Deny
	status := "untrusted"
	if req.Trust {
		dec = trust.Allow
		status = "trusted"
	}
	if err := store.SetBundle(req.Bundle, dec); err != nil {
		return nil, err
	}
	return &SetBundleTrustResult{Status: status, Bundle: req.Bundle}, nil
}

// --- Ref parsing + hashing helpers -------------------------------------------

// parseTrustItemRef splits an item ref "<bundle-ref>#<kind>/<name>" into the
// trust.Ref (repo, bundle path, kind, name, locality), the bundle ref to load
// content from, and any "@<commit>" provenance carried on the bundle ref.
func parseTrustItemRef(ref string) (tRef trust.Ref, loadRef, version string, err error) {
	base, sel, found := strings.Cut(ref, "#")
	if !found || base == "" {
		return trust.Ref{}, "", "", fmt.Errorf("trust ref %q missing #<kind>/<name> selector", ref)
	}
	kind, name, err := parseTrustSelector(sel)
	if err != nil {
		return trust.Ref{}, "", "", fmt.Errorf("invalid trust ref %q: %w", ref, err)
	}

	if parsed, perr := remote.ParseReference(base); perr == nil {
		return trust.Ref{
			RepoURL: parsed.RepoURL(),
			Bundle:  parsed.Path,
			Kind:    kind,
			Name:    name,
			IsLocal: parsed.IsLocal,
		}, base, parsed.ContentVersion, nil
	}

	// base is not a canonical/local ref → treat it as a plain local bundle name.
	return trust.Ref{Bundle: base, Kind: kind, Name: name, IsLocal: true}, base, "", nil
}

// parseTrustSelector parses a "<kind>/<name>" selector (the part after "#").
func parseTrustSelector(sel string) (trust.ItemKind, string, error) {
	kindDir, name, found := strings.Cut(sel, "/")
	if !found || name == "" {
		return "", "", fmt.Errorf("selector %q must be <kind>/<name>", sel)
	}
	switch kindDir {
	case "fragments":
		return trust.KindFragment, name, nil
	case "skills", "prompts":
		// "skills" is the current spelling (the CLI list emits #skills/<name>);
		// "prompts" is the legacy alias. Both map to trust.KindPrompt so the
		// stored key (KindPrompt.Dir() == "prompts"), the assembly-time content
		// gate, and existing grants stay valid — the content lives in
		// bundle.Skills, which computeItemHash reads under KindPrompt.
		return trust.KindPrompt, name, nil
	case "mcp":
		return trust.KindMCP, name, nil
	case "hooks":
		// name is the hook's "<event>/<index>" identity (carries an inner slash).
		return trust.KindHook, name, nil
	default:
		return "", "", fmt.Errorf("unknown item kind %q (want fragments|skills|mcp|hooks)", kindDir)
	}
}

// computeItemHash loads the bundle and computes the item's effective-content
// hash and form, reusing the TR0 hashing primitives. The hash always covers the
// exact bytes that would be exposed (distilled or raw, per cfg), bound to the
// form — never the author-supplied content_hash field.
func computeItemHash(cfg *config.Config, loader *bundles.Loader, tRef trust.Ref, loadRef string) (string, bundles.ContentForm, error) {
	bundle, err := loader.Load(loadRef)
	if err != nil {
		return "", "", err
	}
	// ShouldUseDistilled defaults true; guard a nil cfg so an injected-loader
	// caller (tests) need not construct a full config.
	preferDistilled := true
	if cfg != nil {
		preferDistilled = cfg.ShouldUseDistilled()
	}
	switch tRef.Kind {
	case trust.KindFragment:
		frag, ok := bundle.Fragments[tRef.Name]
		if !ok {
			return "", "", fmt.Errorf("fragment %q not found in bundle %q", tRef.Name, loadRef)
		}
		hash, form := frag.EffectiveContentHash(preferDistilled)
		return hash, form, nil
	case trust.KindPrompt:
		prompt, ok := bundle.Skills[tRef.Name]
		if !ok {
			return "", "", fmt.Errorf("prompt %q not found in bundle %q", tRef.Name, loadRef)
		}
		hash, form := prompt.EffectiveContentHash(preferDistilled)
		return hash, form, nil
	case trust.KindMCP:
		mcp, ok := bundle.MCP[tRef.Name]
		if !ok {
			return "", "", fmt.Errorf("mcp server %q not found in bundle %q", tRef.Name, loadRef)
		}
		return mcp.ComputeContentHash(), bundles.FormRaw, nil
	case trust.KindHook:
		// tRef.Name is the hook's "<event>/<index>" identity (see Entries()).
		entry, ok := bundle.Hooks.EntryByID(tRef.Name)
		if !ok {
			return "", "", fmt.Errorf("hook %q not found in bundle %q", tRef.Name, loadRef)
		}
		return entry.Hook.ComputeContentHash(), bundles.FormRaw, nil
	default:
		return "", "", fmt.Errorf("unknown item kind %q", tRef.Kind)
	}
}

// --- TR3 list-JSON stamping ---------------------------------------------------

// TrustStamper resolves effective per-item trust for a single listing, building
// the trust store, remote registry, and bundle loader ONCE and reusing them
// across every item it stamps. This is TR3's cost control: the cascade is
// content-keyed, so a naive stamp would re-read trust.yaml / remotes.yaml and
// re-materialize each item per call; the stamper reads the stores once and lets
// the shared loader cache each bundle after its first materialization.
//
// It is read-only and fault-tolerant by construction: a build failure or any
// per-item parse/resolve/hash failure never surfaces as an error — it stamps a
// fail-closed DENY (never "trusted"), so a listing can never crash and a hash
// failure can never produce a trusted stamp. Not safe for concurrent use.
type TrustStamper struct {
	cfg      *config.Config
	loader   *bundles.Loader
	store    *trust.Store
	registry *remote.Registry
	fs       afero.Fs

	// denyAll short-circuits every stamp to a fail-closed DENY. Set when the
	// trust store cannot be opened — it may hide a blacklist/denylist we must not
	// silently skip (mirrors EffectiveTrust's corrupt-store posture).
	denyAll bool
}

// TrustStamperOption injects a pre-built dependency, mirroring the loader/
// registry option style. Tests drive the stamper over an in-memory
// store/registry/loader; production builds them from cfg.
type TrustStamperOption func(*TrustStamper)

// WithStampStore injects a pre-built trust store.
func WithStampStore(s *trust.Store) TrustStamperOption {
	return func(ts *TrustStamper) { ts.store = s }
}

// WithStampRegistry injects a pre-built remote registry.
func WithStampRegistry(r *remote.Registry) TrustStamperOption {
	return func(ts *TrustStamper) { ts.registry = r }
}

// WithStampLoader injects a pre-built bundle loader (it must resolve the same
// refs the listing produced).
func WithStampLoader(l *bundles.Loader) TrustStamperOption {
	return func(ts *TrustStamper) { ts.loader = l }
}

// WithStampFS injects the filesystem used to build the store/registry/loader
// when they are not supplied directly.
func WithStampFS(fs afero.Fs) TrustStamperOption {
	return func(ts *TrustStamper) { ts.fs = fs }
}

// NewTrustStamper builds a stamper for cfg. It never errors: if the trust store
// cannot be opened, every subsequent stamp denies (fail closed), matching
// EffectiveTrust. The remote registry is built once on the happy path; if it
// cannot be built it is left nil and resolved per call (still fail-closed at the
// remote tier, while the earlier tiers — denylist/blacklist/grant/bundle/local —
// keep their precedence).
func NewTrustStamper(cfg *config.Config, opts ...TrustStamperOption) *TrustStamper {
	ts := &TrustStamper{cfg: cfg}
	for _, o := range opts {
		o(ts)
	}
	if ts.loader == nil && cfg != nil {
		ts.loader = bundleLoader(cfg)
	}
	if ts.store == nil {
		store, err := getTrustStore(cfg, nil, ts.fs)
		if err != nil {
			clidiag.Warn("ctxloom", "trust store unreadable, denying all stamps: %v", err)
			ts.denyAll = true
		} else {
			ts.store = store
		}
	}
	if ts.registry == nil && !ts.denyAll {
		if reg, err := effectiveTrustRegistry(cfg, EffectiveTrustRequest{FS: ts.fs}); err != nil {
			// Leave nil: EffectiveTrust rebuilds (and fail-closes) per call without
			// disturbing the earlier-tier precedence for items that never reach the
			// remote tier.
			clidiag.Warn("ctxloom", "trust: remote registry unreadable, remote-tier stamps will deny: %v", err)
		} else {
			ts.registry = reg
		}
	}
	return ts
}

// ForRef stamps a fragment/prompt/mcp item addressed by its full list ref
// "<source>#<kind>/<name>". It materializes the item's effective content through
// the shared loader (cached per bundle) to compute the content hash the cascade
// keys on, honoring ShouldUseDistilled. A parse/resolve/hash failure stamps a
// fail-closed DENY (SourceDefault): never trusted, never an error (TR3
// fault-tolerance + fail-closed for the trust signal).
func (ts *TrustStamper) ForRef(ref string) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceDefault}
	}
	tRef, loadRef, _, err := parseTrustItemRef(ref)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceDefault}
	}
	hash, _, err := computeItemHash(ts.cfg, ts.loader, tRef, loadRef)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceDefault}
	}
	return ts.resolve(tRef, hash)
}

// ForLocalMCP stamps a configured (project-local) MCP server, which carries no
// bundle ref. It hashes the server's executable surface
// (BundleMCP.ComputeContentHash — Command+Args+Env+Installation) and resolves it
// as a local mcp item: an executable surface the cascade never auto-trusts, so
// it denies unless an explicit grant, the content denylist, or a bundle posture
// decides otherwise.
func (ts *TrustStamper) ForLocalMCP(name string, srv bundles.BundleMCP) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceDefault}
	}
	ref := trust.Ref{Kind: trust.KindMCP, Name: name, IsLocal: true}
	return ts.resolve(ref, srv.ComputeContentHash())
}

// ForHook stamps a bundle hook addressed by its (source, HookEntry) identity,
// mirroring the exec choke. It hashes the hook's executable surface
// (BundleHook.ComputeContentHash) and resolves it through the cascade. The source
// ref (canonical for a cloned bundle, the local name for a project bundle) is
// parsed so IsLocal/RepoURL are honest — a project-authored hook auto-trusts
// (local tier), a cloned one follows its remote's TrustBundles — the SAME ref
// config.extractHooksFromBundle gates on and `ctxloom trust <src>#hooks/<id>`
// addresses, so the stamped posture is exactly what the gate enforces. It takes
// the in-hand entry rather than re-loading the bundle.
func (ts *TrustStamper) ForHook(source string, entry bundles.HookEntry) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceDefault}
	}
	tRef, _, _, err := parseTrustItemRef(source + "#hooks/" + entry.ID())
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceDefault}
	}
	return ts.resolve(tRef, entry.Hook.ComputeContentHash())
}

// resolve runs the cascade with the stamper's shared store + registry so no
// per-item file I/O happens on the happy path.
func (ts *TrustStamper) resolve(ref trust.Ref, hash string) EffectiveTrustResult {
	res, err := EffectiveTrust(ts.cfg, EffectiveTrustRequest{
		Ref:         ref,
		ContentHash: hash,
		Store:       ts.store,
		Registry:    ts.registry,
		FS:          ts.fs,
	})
	if err != nil || res == nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceDefault}
	}
	return *res
}
