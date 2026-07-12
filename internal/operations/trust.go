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

// EffectiveTrustRequest carries the inputs for the trust decision function.
//
// The caller supplies the item Ref, the exact PAYLOAD BYTES about to be exposed
// (pre-mustache), the Form those bytes are in, and the item's verified Signer.
// Bytes, not a hash: a hash can only ever be compared, and this decision must be
// able to VERIFY — a signature is over bytes, and the store's hash index finds a
// candidate signature but never decides anything (spec §9.3, trap #2).
type EffectiveTrustRequest struct {
	Ref     trust.Ref
	Payload []byte
	// Form names which materialization Payload is (bundles.FormRaw or
	// bundles.FormDistilled, as a string; exec items use FormRaw). An approval
	// only allows when it covers THIS form — an unknown/empty form matches
	// nothing, so it resolves pending (fail closed).
	Form string
	// Signer is the VERIFIED publisher identity of the document this item was
	// parsed out of (bundles.Bundle.Signer): the principal of an allowed_signers
	// entry whose key made a valid publish signature over that document's bytes,
	// or "builtin:ctxloom", or "" for unsigned. It is an INPUT to the decision,
	// never a state of the item — a signed item with an untrusted key is not a
	// fourth state, it is pending (spec §8, red line 1).
	//
	// It is NEVER a claim read from content. See bundles.Bundle.signer.
	Signer string

	Store *trust.Store `json:"-"`
	// Records is the review-record backing store. Optional: nil builds the
	// hash-pair acceptance ledger over Store. This is the S6 seam — see
	// ReviewRecords.
	Records ReviewRecords `json:"-"`
	FS      afero.Fs      `json:"-"`
}

// ReviewRecords is the seam between the DECISION FUNCTION (steps 1 and 5, whose
// shape is settled) and the STORE that records human review decisions (whose
// shape is not).
//
// Today it is backed by the hash-pair acceptance ledger — `.ctxloom/trust.yaml`,
// a plain file that anything able to write it can forge an acceptance into,
// including the agent ctxloom just launched. The countersignature store replaces
// that backing with signatures over the reviewed bytes, at which point a
// hand-edited record becomes inert noise rather than an approval.
//
// The point of this interface is that the swap changes only what implements it.
// Both methods already take the PAYLOAD BYTES rather than a hash, which is the
// shape a signature verification needs and a hash comparison does not — so the
// countersignature implementation slots in without reshaping the decision
// function or any of its call sites.
type ReviewRecords interface {
	// Rejected reports a rejection covering this ref OR exactly these bytes.
	// The two components are deliberately different scopes: the ref block is
	// sticky (it survives the content changing under the ref), while the content
	// rejection is ref-agnostic (a renamed or moved identical copy stays
	// rejected). Both must be honored, and it must be evaluated for EVERY item —
	// including unsigned ones and ones whose publisher signature failed to
	// verify. A rejection is of bytes, not of provenance.
	Rejected(ref trust.Ref, payload []byte) bool

	// Approved reports that a human approved exactly these bytes, at this ref,
	// in this form. Any change to the exposed bytes must drop the approval and
	// return the item to pending.
	Approved(ref trust.Ref, payload []byte, form string) bool
}

// ledgerRecords implements ReviewRecords over the hash-pair acceptance ledger
// (trust.Store). It hashes the payload with the SAME primitive that produced the
// recorded hashes (bundles.HashPayload), so an approval recorded at review time
// and an exposure checked at gate time cannot disagree about what "these bytes"
// means.
//
// TO BE DELETED IN S6, whole, along with the trust.Store hash-pair ledger it
// wraps. Nothing else in the decision function moves.
type ledgerRecords struct {
	store *trust.Store
}

func (l ledgerRecords) Rejected(ref trust.Ref, payload []byte) bool {
	if l.store == nil {
		return false
	}
	// Ref-level block: sticky, survives the content changing under the ref.
	item, found := l.store.Lookup(ref.CanonicalURL(), ref.Key())
	if found && item.State == trust.StateRejected {
		return true
	}
	// Content denylist: repo- and ref-agnostic, so a renamed identical copy
	// stays rejected. An empty payload can never match a recorded hash.
	if len(payload) == 0 {
		return false
	}
	return l.store.DeniedHash(bundles.HashPayload(payload))
}

func (l ledgerRecords) Approved(ref trust.Ref, payload []byte, form string) bool {
	if l.store == nil || len(payload) == 0 {
		return false
	}
	item, found := l.store.Lookup(ref.CanonicalURL(), ref.Key())
	if !found || item.State != trust.StateAccepted {
		return false
	}
	// The recorded hash for THIS form must match. An empty slot or a form
	// mismatch means the exact materialization being exposed was never
	// reviewed, so it stays pending.
	var recorded string
	switch form {
	case string(bundles.FormRaw):
		recorded = item.RawHash
	case string(bundles.FormDistilled):
		recorded = item.DistilledHash
	}
	return recorded != "" && recorded == bundles.HashPayload(payload)
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

// EffectiveTrust is the sole owner of the per-item trust decision function.
// First-match-wins, fail-closed:
//
//  1. REJECTED       DENY   a rejection covers this ref, or these exact bytes
//  2. LOCAL          ALLOW  authored in this project
//  3. BUILTIN        ALLOW  compiled into this binary
//  4. TRUSTED SIGNER ALLOW  a key trusted to PUBLISH signed these bytes
//  5. APPROVED       ALLOW  a human approved exactly these bytes, here, in this form
//  6. otherwise      DENY   pending — withheld until a human reviews it
//
// REJECTION IS SUPREME and it is step 1 for a reason: a user must be able to
// reject content signed by the ctxloom release key itself, and to reject a
// builtin. No signature can un-reject anything, and no verification may
// short-circuit ahead of it — step 1 is evaluated even when the publisher
// signature is absent or failed to verify, because a rejection is of BYTES, not
// of provenance.
//
// Step 4 is where SIGNATURES enter, and what they buy is authentication, never
// authorization: it allows because a key trusted for the publish namespace
// signed exactly these bytes. It replaces the deleted trust_bundles source
// bypass, which allowed because of the URL the bytes arrived from and never
// looked at the content at all. Signed still does not mean safe — that is why
// review (steps 1 and 5) is a separate axis and why rejection outranks every
// signature, including ours.
//
// Red line: NO STATE WAS ADDED. Signer is an input, never a state. An item is
// pending, approved, or rejected; a signed item whose key you do not trust is
// not a fourth thing — it is pending.
//
// Nothing here reads or writes lock.yaml (ADR 0033): the lockfile pins
// dependencies and is not a security surface. Verification happens at the
// EXPOSURE choke, not at fetch or lock time — a pull of an unsigned, badly
// signed, or rejected bundle SUCCEEDS, and its content is withheld here.
func EffectiveTrust(cfg *config.Config, req EffectiveTrustRequest) (*EffectiveTrustResult, error) {
	records := req.Records
	if records == nil {
		store, err := getTrustStore(cfg, req.Store, req.FS)
		if err != nil {
			// A store we cannot read may hold REJECTIONS we would otherwise miss —
			// deny everything rather than silently reopen the gate. Fatal-class in
			// strict mode (a deny-all session is not the session the user set up).
			strictness.Fail(strictness.ClassTrust, "fix or remove .ctxloom/trust.yaml, then re-review (ctxloom review)",
				"trust store unreadable, denying all items: %v", err)
			return decide(trust.Deny, trust.SourcePending), nil
		}
		records = ledgerRecords{store: store}
	}

	// 1. REJECTED. Checked FIRST, ahead of every allow — including the trusted
	//    signer and the builtin. Rejection is supreme.
	if records.Rejected(req.Ref, req.Payload) {
		return decide(trust.Deny, trust.SourceRejected), nil
	}
	// 2. LOCAL: authored in this project — first-party, every kind, including
	//    executables. Locality is honest: a seeded or cloned bundle stamps its
	//    canonical remote ref, so a COPY of remote content keys as remote and is
	//    not local-trusted. "You wrote it here, you trust it; a clone of it is
	//    not yours."
	if req.Ref.IsLocal {
		return decide(trust.Allow, trust.SourceLocal), nil
	}
	// 3. BUILTIN: compiled into this binary. Authenticated BY the binary —
	//    trusting ctxloom trusts what it ships — and deliberately NOT signed
	//    (signing bytes embedded in the binary doing the verifying is circular).
	//    It is a distinct step, below rejection, precisely so step 1 can reach it.
	if req.Ref.IsBuiltin {
		return decide(trust.Allow, trust.SourceBuiltin), nil
	}
	// 4. TRUSTED SIGNER: a key this machine trusts for the PUBLISH namespace made
	//    a signature over the exact bytes of the document this item came from,
	//    and that signature verified — at load, before any YAML parse.
	//
	//    A non-empty Signer means exactly that and cannot mean anything else:
	//    it is stamped only by signing.VerifyPublisher's return value, which
	//    resolves the principal from allowed_signers (never from any advisory
	//    field in the content) and only after checking the key is trusted FOR
	//    THIS NAMESPACE. A bundle cannot declare its own signer; see
	//    bundles.Bundle.signer.
	//
	//    trust.BuiltinSigner is excluded explicitly. It is a SYNTHETIC identity,
	//    not a cryptographic one — nothing about a builtin is verified beyond
	//    "it shipped inside this binary", which is what step 3 above says. A
	//    builtin is allowed as a BUILTIN, at its own step, and never as a
	//    "trusted publisher". Without this guard, any future path that stamped
	//    the synthetic token onto a non-builtin bundle would silently launder it
	//    into a verified publisher, so the guard is here rather than in the
	//    caller's discipline.
	if req.Signer != "" && req.Signer != trust.BuiltinSigner {
		return decide(trust.Allow, trust.SourceTrustedSigner), nil
	}
	// 5. APPROVED: a human reviewed exactly these bytes, at this ref, in this
	//    form. Any change to the exposed bytes drops the approval and returns the
	//    item to pending.
	if records.Approved(req.Ref, req.Payload, req.Form) {
		return decide(trust.Allow, trust.SourceAccepted), nil
	}
	// 6. Terminal fail-closed default: pending, withheld until reviewed. This is
	//    where unsigned content lands, where signed-but-untrusted-key content
	//    lands, and where content whose bytes changed lands.
	return decide(trust.Deny, trust.SourcePending), nil
}

func decide(d trust.Decision, s trust.Source) *EffectiveTrustResult {
	return &EffectiveTrustResult{Decision: d, Source: s}
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

// computeItemPayloadPair loads the bundle and returns the item's (raw,
// distilled) PAYLOAD BYTES — the exact preimages the decision function and any
// signature both key on. rawPayload always covers the raw authored bytes (or the
// canonical executable surface for mcp/hooks, which have no distilled form);
// distilledPayload covers the distilled rewrite and is nil when no distilled
// form exists (or distillation is suppressed via NoDistill).
//
// Bytes are the primitive and hashes are derived from them (computeItemHashPair
// below), never the other way round: there is exactly ONE definition of "the
// bytes of item X in form F" in this codebase — bundles.ContentPayload — and
// everything that needs those bytes, or a hash of them, or a signature over
// them, comes through here. Two definitions is the bug.
//
// It also returns the bundle's VERIFIED publisher identity (empty for unsigned),
// so a caller resolving an item by ref gets the same signer the exposure gate
// would see.
func computeItemPayloadPair(loader *bundles.Loader, tRef trust.Ref, loadRef string) (rawPayload, distilledPayload []byte, signer string, err error) {
	bundle, err := loader.Load(loadRef)
	if err != nil {
		return nil, nil, "", err
	}
	signer = bundle.Signer()

	// payloadPair extracts both form payloads from the shared distillable-item
	// preimage builder: preferDistilled=false always yields the raw form;
	// preferDistilled=true yields the distilled form exactly when one exists.
	payloadPair := func(payload func(bool) ([]byte, bundles.ContentForm)) ([]byte, []byte) {
		raw, _ := payload(false)
		if p, form := payload(true); form == bundles.FormDistilled {
			return raw, p
		}
		return raw, nil
	}
	switch tRef.Kind {
	case trust.KindFragment:
		frag, ok := bundle.Fragments[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("fragment %q not found in bundle %q", tRef.Name, loadRef)
		}
		rawPayload, distilledPayload = payloadPair(frag.ContentPayload)
		return rawPayload, distilledPayload, signer, nil
	case trust.KindPrompt:
		prompt, ok := bundle.Skills[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("prompt %q not found in bundle %q", tRef.Name, loadRef)
		}
		rawPayload, distilledPayload = payloadPair(prompt.ContentPayload)
		return rawPayload, distilledPayload, signer, nil
	case trust.KindMCP:
		mcp, ok := bundle.MCP[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("mcp server %q not found in bundle %q", tRef.Name, loadRef)
		}
		payload, perr := mcp.ContentPayload()
		if perr != nil {
			return nil, nil, "", fmt.Errorf("mcp server %q payload: %w", tRef.Name, perr)
		}
		return payload, nil, signer, nil
	case trust.KindHook:
		// tRef.Name is the hook's "<event>/<index>" identity (see Entries()).
		entry, ok := bundle.Hooks.EntryByID(tRef.Name)
		if !ok {
			return nil, nil, "", fmt.Errorf("hook %q not found in bundle %q", tRef.Name, loadRef)
		}
		payload, perr := entry.Hook.ContentPayload()
		if perr != nil {
			return nil, nil, "", fmt.Errorf("hook %q payload: %w", tRef.Name, perr)
		}
		return payload, nil, signer, nil
	default:
		return nil, nil, "", fmt.Errorf("unknown item kind %q", tRef.Kind)
	}
}

// computeItemHashPair derives the item's (raw, distilled) content-hash pair from
// the payload pair above — the pair the acceptance LEDGER binds to. It exists
// only to serve that ledger (SetItemTrust / SetBlacklist); it is not, and must
// not become, an input to the exposure decision, which keys on bytes.
//
// S6 NOTE: when approvals become countersignatures, this and its two callers'
// hash bookkeeping go with the ledger.
func computeItemHashPair(loader *bundles.Loader, tRef trust.Ref, loadRef string) (rawHash, distilledHash string, err error) {
	rawPayload, distilledPayload, _, err := computeItemPayloadPair(loader, tRef, loadRef)
	if err != nil {
		return "", "", err
	}
	rawHash = bundles.HashPayload(rawPayload)
	if distilledPayload != nil {
		distilledHash = bundles.HashPayload(distilledPayload)
	}
	return rawHash, distilledHash, nil
}

// computeItemPayload resolves the item's CURRENT effective form — distilled when
// cfg prefers it and a distilled form exists, else raw — returning the exact
// bytes assembly would expose, their form, and the bundle's verified signer.
func computeItemPayload(cfg *config.Config, loader *bundles.Loader, tRef trust.Ref, loadRef string) ([]byte, bundles.ContentForm, string, error) {
	rawPayload, distilledPayload, signer, err := computeItemPayloadPair(loader, tRef, loadRef)
	if err != nil {
		return nil, "", "", err
	}
	// ShouldUseDistilled defaults true; guard a nil cfg so an injected-loader
	// caller (tests) need not construct a full config.
	preferDistilled := true
	if cfg != nil {
		preferDistilled = cfg.ShouldUseDistilled()
	}
	if preferDistilled && distilledPayload != nil {
		return distilledPayload, bundles.FormDistilled, signer, nil
	}
	return rawPayload, bundles.FormRaw, signer, nil
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
	cfg     *config.Config
	loader  *bundles.Loader
	store   *trust.Store
	records ReviewRecords
	fs      afero.Fs

	// denyAll short-circuits every stamp to a fail-closed DENY. Set when the
	// trust store cannot be opened — it may hide rejections we must not
	// silently skip (mirrors EffectiveTrust's corrupt-store posture).
	denyAll bool
}

// TrustStamperOption injects a pre-built dependency, mirroring the loader
// option style. Tests drive the stamper over an in-memory store/loader;
// production builds them from cfg.
type TrustStamperOption func(*TrustStamper)

// WithStampStore injects a pre-built trust store.
func WithStampStore(s *trust.Store) TrustStamperOption {
	return func(ts *TrustStamper) { ts.store = s }
}

// WithStampRecords injects a pre-built review-record store, bypassing the
// ledger built over WithStampStore. The S6 seam for listings.
func WithStampRecords(r ReviewRecords) TrustStamperOption {
	return func(ts *TrustStamper) { ts.records = r }
}

// WithStampLoader injects a pre-built bundle loader (it must resolve the same
// refs the listing produced).
func WithStampLoader(l *bundles.Loader) TrustStamperOption {
	return func(ts *TrustStamper) { ts.loader = l }
}

// WithStampFS injects the filesystem used to build the store/loader when they
// are not supplied directly.
func WithStampFS(fs afero.Fs) TrustStamperOption {
	return func(ts *TrustStamper) { ts.fs = fs }
}

// NewTrustStamper builds a stamper for cfg. It never errors: if the trust store
// cannot be opened, every subsequent stamp denies (fail closed), matching
// EffectiveTrust.
func NewTrustStamper(cfg *config.Config, opts ...TrustStamperOption) *TrustStamper {
	ts := &TrustStamper{cfg: cfg}
	for _, o := range opts {
		o(ts)
	}
	if ts.loader == nil && cfg != nil {
		ts.loader = bundleLoader(cfg)
	}
	if ts.records == nil && ts.store == nil {
		store, err := getTrustStore(cfg, nil, ts.fs)
		if err != nil {
			clidiag.Warn("ctxloom", "trust store unreadable, denying all stamps: %v", err)
			ts.denyAll = true
		} else {
			ts.store = store
		}
	}
	return ts
}

// ForRef stamps a fragment/prompt/mcp item addressed by its full list ref
// "<source>#<kind>/<name>". It materializes the item's effective content through
// the shared loader (cached per bundle) to get the exact payload bytes + form +
// verified signer the decision function keys on, honoring ShouldUseDistilled. A
// parse/resolve failure stamps a fail-closed DENY (SourcePending): never
// trusted, never an error (fault tolerance + fail-closed for the trust signal).
func (ts *TrustStamper) ForRef(ref string) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	tRef, loadRef, _, err := parseTrustItemRef(ref)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	payload, form, signer, err := computeItemPayload(ts.cfg, ts.loader, tRef, loadRef)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return ts.resolve(tRef, payload, string(form), signer)
}

// ForLocalMCP stamps a configured (project-local) MCP server, which carries no
// bundle ref and therefore no publisher signature. It resolves the server's
// executable surface (BundleMCP.ContentPayload — Command+Args+Env+Installation)
// as a local mcp item, which the decision function ALLOWS via the first-party
// local exemption at step 2 (the user configured it in this project themselves)
// — unless it has been explicitly rejected at step 1, which beats the exemption.
// Signing is irrelevant here and its absence is not a downgrade: local content
// never needed a key and still does not.
func (ts *TrustStamper) ForLocalMCP(name string, srv bundles.BundleMCP) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	payload, err := srv.ContentPayload()
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	ref := trust.Ref{Kind: trust.KindMCP, Name: name, IsLocal: true}
	return ts.resolve(ref, payload, string(bundles.FormRaw), "")
}

// ForHook stamps a bundle hook addressed by its (source, HookEntry) identity,
// mirroring the exec choke. It resolves the hook's executable surface
// (BundleHook.ContentPayload) through the decision function.
//
// The source ref (canonical for a cloned bundle, the local name for a project
// bundle) is parsed so IsLocal/RepoURL are honest, and the bundle's verified
// SIGNER is resolved through the shared (cached) loader — because the stamp must
// agree with the gate. config.extractHooksFromBundle gates this same hook with
// the bundle's signer in hand; a stamp that omitted it would report "pending"
// for a hook the gate actually exposes, which is a lie in a security display.
func (ts *TrustStamper) ForHook(source string, entry bundles.HookEntry) EffectiveTrustResult {
	if ts.denyAll {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	tRef, loadRef, _, err := parseTrustItemRef(source + "#hooks/" + entry.ID())
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	payload, perr := entry.Hook.ContentPayload()
	if perr != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return ts.resolve(tRef, payload, string(bundles.FormRaw), ts.signerFor(loadRef))
}

// signerFor returns the verified publisher identity of the bundle at loadRef,
// or "" when it is unsigned or cannot be loaded. Fail-safe: an unresolvable
// bundle yields no signer, which means MORE review, never more exposure.
func (ts *TrustStamper) signerFor(loadRef string) string {
	if ts.loader == nil || loadRef == "" {
		return ""
	}
	bundle, err := ts.loader.Load(loadRef)
	if err != nil {
		return ""
	}
	return bundle.Signer()
}

// resolve runs the decision function with the stamper's shared store so no
// per-item file I/O happens on the happy path.
func (ts *TrustStamper) resolve(ref trust.Ref, payload []byte, form, signer string) EffectiveTrustResult {
	res, err := EffectiveTrust(ts.cfg, EffectiveTrustRequest{
		Ref:     ref,
		Payload: payload,
		Form:    form,
		Signer:  signer,
		Store:   ts.store,
		Records: ts.records,
		FS:      ts.fs,
	})
	if err != nil || res == nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return *res
}
