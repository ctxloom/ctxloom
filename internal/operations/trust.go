package operations

import (
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
	"github.com/ctxloom/shared/clidiag"
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
	// 5. Project-authored local CONTENT (fragment/prompt) is auto-allowed. Local
	//    executables (mcp / hooks) are deliberately NOT — they fall through.
	if req.Ref.IsLocal && req.Ref.Kind.IsContent() {
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
	// kind is fragments|prompts|mcp. A trailing "@<commit>" on the bundle ref is
	// recorded as provenance.
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
	case "prompts":
		return trust.KindPrompt, name, nil
	case "mcp":
		return trust.KindMCP, name, nil
	default:
		return "", "", fmt.Errorf("unknown item kind %q (want fragments|prompts|mcp)", kindDir)
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
		prompt, ok := bundle.Prompts[tRef.Name]
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
	default:
		return "", "", fmt.Errorf("unknown item kind %q", tRef.Kind)
	}
}
