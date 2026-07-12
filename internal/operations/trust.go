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
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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

// EffectiveTrustRequest carries the inputs for the trust decision function. The
// caller supplies the item Ref, the content hash of the bytes actually being
// exposed, and the Form those bytes are in ("raw" | "distilled") — the resolver
// never fetches or hashes, that is the caller's job. Store/Registry/FS are
// optional injection points for testing.
type EffectiveTrustRequest struct {
	Ref         trust.Ref
	ContentHash string
	// Form names which materialization ContentHash covers (bundles.FormRaw or
	// bundles.FormDistilled, as a string). An accepted item only allows when the
	// recorded hash for THIS form matches — an unknown/empty form matches no
	// slot, so it resolves pending (fail closed).
	Form string

	Store    *trust.Store     `json:"-"`
	Registry *remote.Registry `json:"-"`
	FS       afero.Fs         `json:"-"`
}

// EffectiveTrustResult reports the decision outcome and which step decided it.
type EffectiveTrustResult struct {
	Decision trust.Decision `json:"decision"`
	Source   trust.Source   `json:"source"`
}

// Trusted reports whether the decision allowed exposure. It is the boolean the
// list-JSON stamp surfaces as "trusted"; Source (as a plain string) is the
// companion "trust_source".
func (r EffectiveTrustResult) Trusted() bool {
	return r.Decision == trust.Allow
}

// State renders the result in the three-state review vocabulary for listings:
// rejected when a rejection decided it, accepted for any allow (a reviewed
// acceptance or a first-party exemption — Source says which), and pending for
// every other deny (awaiting review, or fail-closed).
func (r EffectiveTrustResult) State() trust.State {
	switch {
	case r.Source == trust.SourceRejected:
		return trust.StateRejected
	case r.Decision == trust.Allow:
		return trust.StateAccepted
	default:
		return trust.StatePending
	}
}

// EffectiveTrust is the sole owner of the per-item trust decision function
// (trust-simplify). It unifies the review-state store with the trusted-sources
// set (remotes.yaml's TrustBundles) at read time, evaluating exactly:
//
//	DENY  if state rejected OR content hash denylisted   (rejected)
//	ALLOW if the item is local (project-authored)         (local)
//	ALLOW if the item is a builtin (shipped in the binary) (builtin)
//	ALLOW if the repo is a trusted source                 (trusted-source)
//	ALLOW if accepted AND current-form hash matches       (accepted)
//	else DENY                                             (pending)
//
// Rejection is checked first so it beats every exemption — local, builtin, and
// trusted source alike (a user can reject an item even from a trusted source or
// a builtin). It is fail-closed: a corrupt/unreadable trust store or remote
// registry denies rather than degrading to allow-by-default, and any step that
// cannot positively justify exposure falls through to the terminal pending-DENY.
func EffectiveTrust(cfg *config.Config, req EffectiveTrustRequest) (*EffectiveTrustResult, error) {
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		// A trust store we cannot read may hold rejections we would otherwise
		// miss — deny everything rather than silently reopen it. Fatal-class in
		// strict mode (a deny-all session is not the session the user set up).
		strictness.Fail(strictness.ClassTrust, "fix or remove .ctxloom/trust.yaml, then re-review (ctxloom review)",
			"trust store unreadable, denying all items: %v", err)
		return decide(trust.Deny, trust.SourcePending), nil
	}

	repoURL := req.Ref.CanonicalURL()
	refKey := req.Ref.Key()

	// 1. Rejected — the ref's recorded state, or the exact content on the
	//    repo/ref-agnostic denylist (a renamed identical copy stays rejected).
	//    Beats every exemption, including local and trusted sources.
	item, found := store.Lookup(repoURL, refKey)
	if (found && item.State == trust.StateRejected) || store.DeniedHash(req.ContentHash) {
		return decide(trust.Deny, trust.SourceRejected), nil
	}
	// 2. Project-authored LOCAL items are first-party — every kind, including
	//    executables (config-level MCP, ctxloom:local, and project-bundle
	//    hooks/MCP). Locality is honest here: the gate and stamps key bundle
	//    items by their source ref (canonical for a cloned bundle → IsLocal
	//    false, so a clone falls through to the review states). "You authored it
	//    in this project, so it is trusted; a clone is not."
	if req.Ref.IsLocal {
		return decide(trust.Allow, trust.SourceLocal), nil
	}
	// 3. Builtin: shipped inside this binary (resources/builtin_bundles).
	//    Authenticated by the binary itself — trusting ctxloom trusts what it
	//    ships — so allowed by default with no review friction. Unlike the
	//    prior gate=nil bypass, this step is reachable: step 1's rejection
	//    check already ran, so a user's rejection of a builtin item is
	//    enforced (trust-model.md: rejection beats even a builtin).
	if req.Ref.IsBuiltin {
		return decide(trust.Allow, trust.SourceBuiltin), nil
	}
	// 4. Trusted source: everything the repo publishes — updates included — is
	//    exempt from review (remotes.yaml TrustBundles membership).
	registry, rerr := effectiveTrustRegistry(cfg, req)
	if rerr != nil {
		clidiag.Warn("ctxloom", "trust: remote registry unreadable, denying: %v", rerr)
		return decide(trust.Deny, trust.SourcePending), nil
	}
	if trusted, ok := remoteTrusted(registry, repoURL); ok && trusted {
		return decide(trust.Allow, trust.SourceTrustedSource), nil
	}
	// 5. Accepted at the current hash: a human reviewed exactly these bytes.
	//    The recorded hash for the CURRENT form must match — an empty slot (lazy
	//    v1 migration recorded only one form) or a form mismatch means this
	//    exact materialization was never reviewed, so it stays pending.
	if found && item.State == trust.StateAccepted && req.ContentHash != "" {
		var recorded string
		switch req.Form {
		case string(bundles.FormRaw):
			recorded = item.RawHash
		case string(bundles.FormDistilled):
			recorded = item.DistilledHash
		}
		if recorded != "" && recorded == req.ContentHash {
			return decide(trust.Allow, trust.SourceAccepted), nil
		}
	}
	// 6. Terminal fail-closed default: pending, withheld until reviewed.
	return decide(trust.Deny, trust.SourcePending), nil
}

func decide(d trust.Decision, s trust.Source) *EffectiveTrustResult {
	return &EffectiveTrustResult{Decision: d, Source: s}
}

// effectiveTrustRegistry returns the remote registry for the trusted-source
// step, building it lazily so the earlier (store-only) steps never touch the
// registry fs. Honors an injected registry for testing.
func effectiveTrustRegistry(cfg *config.Config, req EffectiveTrustRequest) (*remote.Registry, error) {
	if req.Registry != nil {
		return req.Registry, nil
	}
	return getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS)))
}

// remoteTrusted reports whether a remote whose canonical URL matches canonicalURL
// is registered, and whether it carries TrustBundles (trusted-sources set
// membership). Both sides are canonicalized through the SAME function so a
// registered remote and the ref's repo URL cannot diverge on a spelling variant.
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

// --- Mutations (the plumbing under `ctxloom trust` / `ctxloom blacklist`) -----

// SetItemTrustRequest accepts the currently-resolved version of an item.
type SetItemTrustRequest struct {
	// Ref is the item reference, "<bundle-ref>#<kind>/<name>" where bundle-ref
	// is a canonical URL ref, a ctxloom:local ref, or a plain local bundle name,
	// kind is fragments|skills|mcp|hooks (legacy "prompts" still accepted). A
	// trailing "@<commit>" on the bundle ref is accepted for resolution;
	// acceptance pins by content hash, not commit.
	Ref string

	Store  *trust.Store    `json:"-"`
	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetItemTrustResult reports the recorded acceptance.
type SetItemTrustResult struct {
	Status        string `json:"status"` // "accepted"
	Ref           string `json:"ref"`
	RepoURL       string `json:"repo_url"`
	RawHash       string `json:"raw_hash"`
	DistilledHash string `json:"distilled_hash,omitempty"` // empty when no distilled form exists
}

// SetItemTrust records an item as accepted, bound to BOTH of the item's current
// form hashes (raw always; distilled when a distilled form exists), so a later
// change to either exposed form returns the item to pending and forces
// re-review. The hashes are always recomputed from the resolved content — never
// read from the author-supplied content_hash field. Alongside the store write
// it snapshots the accepted bytes (content kinds only, best-effort) so a later
// upstream change can be reviewed as a diff — see snapshotAcceptedItemContent.
func SetItemTrust(cfg *config.Config, req SetItemTrustRequest) (*SetItemTrustResult, error) {
	tRef, loadRef, _, err := parseTrustItemRef(req.Ref)
	if err != nil {
		return nil, err
	}
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}
	rawHash, distilledHash, err := computeItemHashPair(loader, tRef, loadRef)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %q to accept it: %w", req.Ref, err)
	}
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		return nil, err
	}
	if err := store.SetAccepted(tRef.CanonicalURL(), tRef.Key(), rawHash, distilledHash); err != nil {
		return nil, err
	}
	snapshotAcceptedItemContent(cfg, loader, tRef, loadRef, req.FS, rawHash, distilledHash)
	return &SetItemTrustResult{
		Status:        "accepted",
		Ref:           tRef.Key(),
		RepoURL:       tRef.CanonicalURL(),
		RawHash:       rawHash,
		DistilledHash: distilledHash,
	}, nil
}

// SetBlacklistRequest rejects an item.
type SetBlacklistRequest struct {
	Ref string

	Store  *trust.Store    `json:"-"`
	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetBlacklistResult reports the recorded rejection.
type SetBlacklistResult struct {
	Status  string `json:"status"` // "rejected"
	Ref     string `json:"ref"`
	RepoURL string `json:"repo_url"`
	// ContentHashes are the denylisted content hashes (raw + distilled when
	// present); empty if the item could not be resolved.
	ContentHashes []string `json:"content_hashes,omitempty"`
}

// SetBlacklist records BOTH companion components of a rejection: the ref-level
// rejected state (denies this ref regardless of content/version, surviving
// changes) AND the item's current content hashes — raw and distilled — on the
// content denylist (so a renamed/moved identical copy stays rejected). The
// ref-level state is written even when the item cannot be resolved (e.g.
// already deleted) — the hashes are then simply omitted from the denylist.
func SetBlacklist(cfg *config.Config, req SetBlacklistRequest) (*SetBlacklistResult, error) {
	tRef, loadRef, _, err := parseTrustItemRef(req.Ref)
	if err != nil {
		return nil, err
	}
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}
	// Best-effort hashes: rejecting must not fail just because the content is
	// gone — the ref-level rejected state is the durable guarantee.
	var hashes []string
	if rawHash, distilledHash, herr := computeItemHashPair(loader, tRef, loadRef); herr == nil {
		for _, h := range []string{rawHash, distilledHash} {
			if h != "" {
				hashes = append(hashes, h)
			}
		}
	}
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		return nil, err
	}
	if err := store.SetRejected(tRef.CanonicalURL(), tRef.Key(), hashes...); err != nil {
		return nil, err
	}
	return &SetBlacklistResult{
		Status:        "rejected",
		Ref:           tRef.Key(),
		RepoURL:       tRef.CanonicalURL(),
		ContentHashes: hashes,
	}, nil
}

// --- Ref parsing + hashing helpers -------------------------------------------

// builtinSourcePrefix marks a builtin-bundle source ref, e.g. "builtin:ltk"
// (the exact "builtin:"+name string extractMCPFromBundle/extractHooksFromBundle/
// ResolveBuiltinBundleFragments construct as their gate ref's bundle component —
// see config_bundles.go). It is never produced by anything reading user- or
// remote-controlled input: only the three builtin resolvers, fed exclusively
// from resources.GetBuiltinBundle (compiled into the binary), construct it.
const builtinSourcePrefix = "builtin:"

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

	// base failed to parse as a canonical/local ref. A builtin bundle's source
	// ref is recognized explicitly (never falls through to the local guess
	// below) so a builtin item carries its own identity in the trust store —
	// distinct from "local" — and is reachable by the rejection step (trust
	// rework: builtins used to bypass the gate entirely, gate=nil).
	if bundle, ok := strings.CutPrefix(base, builtinSourcePrefix); ok {
		return trust.Ref{Bundle: bundle, Kind: kind, Name: name, IsBuiltin: true}, base, "", nil
	}

	// base is still unrecognized. A genuinely local bundle is referenced by a
	// bare name carrying NO scheme marker at all (e.g. "my-tools", "lang/go") —
	// that is the only case this may still resolve to local. Anything that
	// LOOKS like an attempted canonical/local/builtin ref (a URL scheme, a
	// git@ prefix, or the ctxloom:local@ prefix) but failed to parse must NOT
	// be silently downgraded to "local": that would let an unrecognized or
	// malformed source ref bypass the trust gate entirely (the fail-open bug
	// this fixes — a seeded remote bundle whose canonical ref somehow fails to
	// parse must fail CLOSED, not open). Every caller of parseTrustItemRef
	// already treats an error as fail-closed (the content/exec gates withhold,
	// TrustStamper stamps pending, the CLI mutations refuse the operation), so
	// erroring here is safe in every call site.
	if looksLikeSourceRef(base) {
		return trust.Ref{}, "", "", fmt.Errorf(
			"trust ref %q: %q is not a valid canonical or ctxloom:local reference "+
				"(and not a builtin source) — refusing to treat an unrecognized source as local", ref, base)
	}

	// base is a bare token with no scheme marker → a plain local bundle name.
	return trust.Ref{Bundle: base, Kind: kind, Name: name, IsLocal: true}, base, "", nil
}

// looksLikeSourceRef reports whether s carries a marker that indicates it was
// INTENDED as a scheme-qualified reference — a canonical URL (contains "://"),
// an SSH ref (git@ prefix), or a ctxloom:local ref (ctxloom:local@ prefix) —
// as opposed to a bare local bundle name, which carries none of these. It is
// the fail-closed/fail-open boundary for parseTrustItemRef's fallback: a
// string that looks like it was meant to be a source ref but doesn't parse
// must never be treated as local.
func looksLikeSourceRef(s string) bool {
	return strings.Contains(s, "://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, remote.LocalSource+"@")
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
		// gate, and existing acceptances stay valid — the content lives in
		// bundle.Skills, which the hash helpers read under KindPrompt.
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

// computeItemHashPair loads the bundle and computes the item's (raw, distilled)
// content-hash pair — the pair an acceptance binds to. rawHash always covers
// the raw authored bytes (or the executable surface for mcp/hooks, which have
// no distilled form); distilledHash covers the distilled rewrite and is empty
// when no distilled form exists (or distillation is suppressed via NoDistill).
// The hashes always cover the exact bytes that would be exposed in each form —
// never the author-supplied content_hash field.
func computeItemHashPair(loader *bundles.Loader, tRef trust.Ref, loadRef string) (rawHash, distilledHash string, err error) {
	bundle, err := loader.Load(loadRef)
	if err != nil {
		return "", "", err
	}
	// hashPair extracts both form hashes from the shared distillable-item hash
	// primitive: preferDistilled=false always yields the raw form;
	// preferDistilled=true yields the distilled form exactly when one exists.
	hashPair := func(effective func(bool) (string, bundles.ContentForm)) (string, string) {
		raw, _ := effective(false)
		if h, form := effective(true); form == bundles.FormDistilled {
			return raw, h
		}
		return raw, ""
	}
	switch tRef.Kind {
	case trust.KindFragment:
		frag, ok := bundle.Fragments[tRef.Name]
		if !ok {
			return "", "", fmt.Errorf("fragment %q not found in bundle %q", tRef.Name, loadRef)
		}
		rawHash, distilledHash = hashPair(frag.EffectiveContentHash)
		return rawHash, distilledHash, nil
	case trust.KindPrompt:
		prompt, ok := bundle.Skills[tRef.Name]
		if !ok {
			return "", "", fmt.Errorf("prompt %q not found in bundle %q", tRef.Name, loadRef)
		}
		rawHash, distilledHash = hashPair(prompt.EffectiveContentHash)
		return rawHash, distilledHash, nil
	case trust.KindMCP:
		mcp, ok := bundle.MCP[tRef.Name]
		if !ok {
			return "", "", fmt.Errorf("mcp server %q not found in bundle %q", tRef.Name, loadRef)
		}
		return mcp.ComputeContentHash(), "", nil
	case trust.KindHook:
		// tRef.Name is the hook's "<event>/<index>" identity (see Entries()).
		entry, ok := bundle.Hooks.EntryByID(tRef.Name)
		if !ok {
			return "", "", fmt.Errorf("hook %q not found in bundle %q", tRef.Name, loadRef)
		}
		return entry.Hook.ComputeContentHash(), "", nil
	default:
		return "", "", fmt.Errorf("unknown item kind %q", tRef.Kind)
	}
}

// computeItemHash resolves the item's CURRENT effective form — distilled when
// cfg prefers it and a distilled form exists, else raw — returning its hash and
// form: the same bytes assembly would expose.
func computeItemHash(cfg *config.Config, loader *bundles.Loader, tRef trust.Ref, loadRef string) (string, bundles.ContentForm, error) {
	rawHash, distilledHash, err := computeItemHashPair(loader, tRef, loadRef)
	if err != nil {
		return "", "", err
	}
	// ShouldUseDistilled defaults true; guard a nil cfg so an injected-loader
	// caller (tests) need not construct a full config.
	preferDistilled := true
	if cfg != nil {
		preferDistilled = cfg.ShouldUseDistilled()
	}
	if preferDistilled && distilledHash != "" {
		return distilledHash, bundles.FormDistilled, nil
	}
	return rawHash, bundles.FormRaw, nil
}

// --- list-JSON stamping -------------------------------------------------------

// TrustStamper resolves effective per-item trust for a single listing, building
// the trust store, remote registry, and bundle loader ONCE and reusing them
// across every item it stamps. This is the listing cost control: the decision
// function is content-keyed, so a naive stamp would re-read trust.yaml /
// remotes.yaml and re-materialize each item per call; the stamper reads the
// stores once and lets the shared loader cache each bundle after its first
// materialization.
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
	// trust store cannot be opened — it may hide rejections we must not
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
// trusted-source step, while the earlier steps — rejected/local — keep their
// precedence).
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
			// disturbing the earlier-step precedence for items that never reach the
			// trusted-source step.
			clidiag.Warn("ctxloom", "trust: remote registry unreadable, source-step stamps will deny: %v", err)
		} else {
			ts.registry = reg
		}
	}
	return ts
}

// ForRef stamps a fragment/prompt/mcp item addressed by its full list ref
// "<source>#<kind>/<name>". It materializes the item's effective content through
// the shared loader (cached per bundle) to compute the content hash + form the
// decision function keys on, honoring ShouldUseDistilled. A parse/resolve/hash
// failure stamps a fail-closed DENY (SourcePending): never trusted, never an
// error (fault tolerance + fail-closed for the trust signal).
func (ts *TrustStamper) ForRef(ref string) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	tRef, loadRef, _, err := parseTrustItemRef(ref)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	hash, form, err := computeItemHash(ts.cfg, ts.loader, tRef, loadRef)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return ts.resolve(tRef, hash, string(form))
}

// ForLocalMCP stamps a configured (project-local) MCP server, which carries no
// bundle ref. It hashes the server's executable surface
// (BundleMCP.ComputeContentHash — Command+Args+Env+Installation) and resolves it
// as a local mcp item, which the decision function ALLOWS via the first-party
// local exemption (the user configured it in this project themselves) — unless
// it has been explicitly rejected (ref state or content denylist), which beats
// the exemption.
func (ts *TrustStamper) ForLocalMCP(name string, srv bundles.BundleMCP) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	ref := trust.Ref{Kind: trust.KindMCP, Name: name, IsLocal: true}
	return ts.resolve(ref, srv.ComputeContentHash(), string(bundles.FormRaw))
}

// ForHook stamps a bundle hook addressed by its (source, HookEntry) identity,
// mirroring the exec choke. It hashes the hook's executable surface
// (BundleHook.ComputeContentHash) and resolves it through the decision function.
// The source ref (canonical for a cloned bundle, the local name for a project
// bundle) is parsed so IsLocal/RepoURL are honest — a project-authored hook is
// first-party (local exemption), a cloned one follows its review state — the
// SAME ref config.extractHooksFromBundle gates on and `ctxloom trust
// <src>#hooks/<id>` addresses, so the stamped posture is exactly what the gate
// enforces. It takes the in-hand entry rather than re-loading the bundle.
func (ts *TrustStamper) ForHook(source string, entry bundles.HookEntry) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	tRef, _, _, err := parseTrustItemRef(source + "#hooks/" + entry.ID())
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return ts.resolve(tRef, entry.Hook.ComputeContentHash(), string(bundles.FormRaw))
}

// resolve runs the decision function with the stamper's shared store + registry
// so no per-item file I/O happens on the happy path.
func (ts *TrustStamper) resolve(ref trust.Ref, hash, form string) EffectiveTrustResult {
	res, err := EffectiveTrust(ts.cfg, EffectiveTrustRequest{
		Ref:         ref,
		ContentHash: hash,
		Form:        form,
		Store:       ts.store,
		Registry:    ts.registry,
		FS:          ts.fs,
	})
	if err != nil || res == nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return *res
}
