package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// EffectiveTrustResult reports the decision outcome and which step decided it.
type EffectiveTrustResult struct {
	Decision trust.Decision `json:"decision"`
	Source   trust.Source   `json:"source"`
	// Detail is an OPTIONAL human-readable elaboration on Source, display-only
	// (never a decision input). Today only step 2 (retraction) populates it,
	// with the publisher's stated retraction reason (see
	// RetractionRecords.Retracted) — the same untrusted, informational string
	// `ctxloom deps pull`'s "Retracted:" bucket already surfaces at sync
	// time (internal/adapters/cli/remote.go). Empty for every other Source.
	Detail string `json:"detail,omitempty"`
}

// Trusted reports whether the decision allowed exposure. It is the boolean the
// list-JSON stamp surfaces as "trusted"; Source (as a plain string) is the
// companion "trust_source".
func (r EffectiveTrustResult) Trusted() bool {
	return r.Decision == trust.Allow
}

// State renders the result in the three-state review vocabulary for listings:
// rejected when a rejection OR a retraction decided it (both withhold
// permanently, pending nothing further), accepted for any allow (a reviewed
// acceptance or a first-party exemption — Source says which), and pending for
// every other deny (awaiting review, or fail-closed).
func (r EffectiveTrustResult) State() trust.State {
	switch {
	case r.Source == trust.SourceRejected || r.Source == trust.SourceRetracted:
		return trust.StateRejected
	case r.Decision == trust.Allow:
		return trust.StateAccepted
	default:
		return trust.StatePending
	}
}

// Reason renders a short, content-free, human-readable explanation of WHY a
// withheld item was withheld — deriving entirely from the already-computed
// Source/Detail (never recomputing or re-deriving the decision). This is the
// single place that turns a Source into user-facing words, so every withheld
// advisory (the assembly-time warnWithheld, ExecutableTrustGate.WarnWithheld)
// says the same thing for the same Source. A withhold must never be silent or
// reasonless (see docs/trust-model.md): every caller printing a withheld ref
// pairs it with this string.
//
// Only the three DENY sources are meaningful here (Reason is only ever
// consulted for a withheld item); the default case covers them defensively.
func (r EffectiveTrustResult) Reason() string {
	switch r.Source {
	case trust.SourceRejected:
		return "rejected"
	case trust.SourceRetracted:
		if r.Detail != "" {
			return fmt.Sprintf("retracted by the publisher (%s)", r.Detail)
		}
		return "retracted by the publisher"
	default:
		// trust.SourcePending, and the fail-closed default for any future
		// deny source that forgets to add a case here — pending review is
		// the safe, actionable default, never a bare "withheld". Wording
		// ("awaiting review") matches warnPendingTally's existing phrasing —
		// tests/acceptance/steps_j000200_setup.go's "Alice is told the content is
		// held for her review" step asserts on this exact substring.
		return "awaiting review — run 'ctxloom review'"
	}
}

// resolveCountersignStore picks the physical countersignature store a
// mutation writes to: the committable PROJECT store when project is true,
// else the personal USER store (spec §9.2). Injected stores (test seams) win
// outright; production builds the real on-disk store from cfg.
func resolveCountersignStore(cfg *config.Config, fs afero.Fs, project bool, injectedUser, injectedProject *countersign.Store) (store *countersign.Store, name string, err error) {
	f := getFS(fs)
	if project {
		if injectedProject != nil {
			return injectedProject, "project", nil
		}
		return countersign.NewStore(paths.ApprovalsPath(getBaseDir(cfg)), f), "project", nil
	}
	if injectedUser != nil {
		return injectedUser, "user", nil
	}
	home, herr := countersign.HomeDir()
	if herr != nil {
		return nil, "", fmt.Errorf("cannot resolve the user approvals store: %w", herr)
	}
	return countersign.NewStore(home, f), "user", nil
}

// resolveSignerOrUnsigned resolves the key a review mutation countersigns
// with. An injected signer always wins (tests, and any caller that already
// resolved one). Otherwise it runs the unified zero-config discovery chain
// (internal/adapters/signing/agentkey: explicit key, then `git config
// user.signingkey`, then the sole ssh-agent identity — spec §7A.4).
//
// Failing that: a PROJECT-store write hard-errors — spec §9.5 is explicit that
// `ctxloom review --project` "requires a key and refuses to run without one",
// because an unsigned record in a COMMITTABLE store would be a forgery
// primitive with a friendly name. A USER-store write instead degrades to the
// UNSIGNED path (ok=true, unsigned=true) — exactly as safe as the deleted
// trust.yaml design and never silently promoted to the shared store. This
// applies uniformly whether discovery failed with "no key anywhere"
// (*agentkey.NoKeyError) or "ambiguous, didn't guess" (*agentkey.AmbiguousKeyError).
func resolveSignerOrUnsigned(cfg *config.Config, injected ssh.Signer, project bool) (signer ssh.Signer, unsigned bool, err error) {
	if injected != nil {
		return injected, false, nil
	}
	// sign.key feeds the chain's explicit-key slot here exactly as it does for
	// `ctxloom sign` and `ctxloom review`. Omitting it made the trust/blacklist
	// plumbing disagree with the porcelain that calls it.
	var explicitKey string
	if cfg != nil {
		explicitKey = cfg.SignKey()
	}
	discoverer, err := SignerDiscoverer()
	if err != nil {
		return nil, false, err
	}
	resolved, err := ResolveLocalSigner(context.Background(), discoverer, explicitKey, project)
	var refused *NoSigningKeyError
	if errors.As(err, &refused) {
		return nil, false, remedyf(refused,
			"no signing key available (%w) — the project store requires a signed countersignature; "+
				"run 'ssh-add' and try again, or record this decision in the personal store instead")
	}
	if err != nil {
		return nil, false, err
	}
	return resolved.Signer, resolved.Unsigned, nil
}

// reviewTrustRoot resolves the trust root a review MUTATION authorizes its
// signing key against. It mirrors buildCountersignRecords' own root resolution
// exactly — injected wins, else cfg's full root (embedded + user + project
// allowed_signers), else an empty store — because the writer and the reader
// must consult the SAME root or the writer will happily record decisions the
// reader can never honour. That divergence is the whole bug this exists to
// close.
//
// An empty store trusts nothing, which is the fail-closed answer: "I could not
// establish that this key may decide here" refuses, it never accepts.
func reviewTrustRoot(cfg *config.Config, injected trust.TrustRoot) trust.TrustRoot {
	if injected != nil {
		return injected
	}
	if cfg != nil {
		return cfg.TrustRoot()
	}
	return allowedsigners.NewStore()
}

// resolveDecisionSigner resolves the key a review mutation countersigns with
// AND authorizes it for the namespace that decision will be asserted in.
//
// The authorization is folded in here, rather than left as a separate
// pre-check at each call site, for the same reason VerifyPublisher gates its
// cryptographic verification on the namespace check: a caller that could
// obtain a signer without naming the assertion could forget to ask whether
// that key may make it. Here you cannot get a signer without declaring what it
// is about to sign.
//
// THE BUG THIS CLOSES (taskloom tiny-bankbook). A countersignature is honoured
// by EffectiveTrust step 1/6 only when its signer is trusted for the
// approve/reject namespace — VerifyCountersignature checks
// TrustedForNamespace before it verifies a single byte, and answers a flat
// "not countersigned" when the key is not trusted. Writing such a record
// therefore produced a well-formed file, a success line naming the key, exit
// 0 — and an item that stayed withheld, with nothing anywhere saying why. The
// namespace check now runs on the WRITE side too, against the same root and
// through the same signing.NamespaceForAssertion derivation the verifier
// uses, so the two can never disagree about which namespace a decision needs.
//
// The UNSIGNED degraded path (spec §9.5) is deliberately untouched: it has no
// key, so there is no namespace question to ask, and its records are honoured
// by their own path (HasUnsignedApprove / HasUnsignedRefReject) which consults
// no trust root at all. Only the signing-key-present path is authorized here.
func resolveDecisionSigner(cfg *config.Config, injected ssh.Signer, project bool, root trust.TrustRoot, assertion signing.Assertion) (signer ssh.Signer, unsigned bool, err error) {
	signer, unsigned, err = resolveSignerOrUnsigned(cfg, injected, project)
	if err != nil || unsigned {
		return signer, unsigned, err
	}
	if err := requireTrustedForAssertion(reviewTrustRoot(cfg, root), signer.PublicKey(), assertion); err != nil {
		return nil, false, err
	}
	return signer, false, nil
}

// ErrReviewKeyUntrusted reports that a review decision was REFUSED because the
// key it would have been countersigned with is not trusted for that decision's
// namespace. Recording it anyway is the silent no-op described on
// resolveDecisionSigner; refusing is the fail-loud alternative. Exported so
// callers and tests can match on the condition rather than on message text.
var ErrReviewKeyUntrusted = errors.New("review key not trusted for this decision's namespace")

// requireTrustedForAssertion refuses unless root authorizes key to make
// assertion, right now.
//
// Fail-closed in every arm: an assertion outside the closed vocabulary, a nil
// root, and an untrusted key all refuse. There is no arm that grants except
// an explicit TrustedForNamespace yes.
func requireTrustedForAssertion(root trust.TrustRoot, key ssh.PublicKey, assertion signing.Assertion) error {
	ns := signing.NamespaceForAssertion(assertion)
	if ns == "" {
		// Unreachable from the two production call sites (both pass a literal
		// from the closed vocabulary), and refused rather than defaulted so a
		// future third assertion cannot acquire authorization by omission.
		return fmt.Errorf("%w: no namespace is defined for the %q assertion, so no key can be authorized to make it", ErrReviewKeyUntrusted, assertion)
	}
	if key == nil {
		return fmt.Errorf("%w: the signing key exposes no public key, so it cannot be checked against the trust root", ErrReviewKeyUntrusted)
	}
	if root != nil && root.TrustedForNamespace(key, ns, time.Now()).Trusted {
		return nil
	}
	return fmt.Errorf(
		"%w: %s is not trusted for the %q namespace.\n"+
			"Recording this decision anyway would write a countersignature nothing honours: the command would report success and the item would stay withheld, with no sign that anything went wrong.\n"+
			"Trust this key to make review decisions, then run this again:\n"+
			"    ctxloom signer trust <you@example.com> --key <path/to/your_key.pub> --namespace approve,reject\n"+
			"(add --project to record the grant in the committable project store). "+
			"With no signing key at all, decisions are recorded unsigned in your personal store instead",
		ErrReviewKeyUntrusted, ssh.FingerprintSHA256(key), ns)
}

// SetItemTrustRequest accepts the currently-resolved version of an item.
type SetItemTrustRequest struct {
	// Ref is the item reference, "<bundle-ref>#<kind>/<name>" where bundle-ref
	// is a canonical URL ref, a ctxloom:local ref, or a plain local bundle name,
	// kind is fragments|commands|mcp|hooks|skills (legacy "prompts" still
	// accepted as an alias for commands). A
	// trailing "@<commit>" on the bundle ref is accepted for resolution;
	// approval pins by content BYTES (a countersignature), not commit.
	Ref string

	// Project writes to the COMMITTABLE project store (spec §9.2) instead of
	// the personal user store. Requires a resolvable signing key — see
	// resolveSignerOrUnsigned.
	Project bool

	// Signer overrides key resolution (test injection). Nil resolves via
	// ssh-agent, falling back to the unsigned degraded path for the user
	// store (never for Project).
	Signer ssh.Signer `json:"-"`

	// UserStore / ProjectStore override the physical countersignature stores
	// (test injection); production builds them from cfg.
	UserStore    *countersign.Store `json:"-"`
	ProjectStore *countersign.Store `json:"-"`

	// Root overrides the trust root the SIGNING KEY is authorized against
	// (test injection, mirroring buildCountersignRecords' injectedRoot);
	// production resolves it from cfg. See resolveDecisionSigner: a key not
	// trusted for the approve namespace cannot record an approval anyone would
	// honour, so it is refused rather than written.
	Root trust.TrustRoot `json:"-"`

	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetItemTrustResult reports the recorded approval.
type SetItemTrustResult struct {
	Status  string `json:"status"` // "approved"
	Ref     string `json:"ref"`
	RepoURL string `json:"repo_url"`
	// KeyFingerprint is the countersigning key's SHA256 fingerprint, empty
	// when the approval was recorded UNSIGNED (spec §9.5).
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	Unsigned       bool   `json:"unsigned,omitempty"`
	// Store names which physical store received the countersignature: "user"
	// (personal, default) or "project" (committable).
	Store string `json:"store"`
}

// SetItemTrust records an item as approved by COUNTERSIGNING its current
// content bytes: a signature over the raw form always, and — when the item
// has a distilled form — a SECOND signature over the distilled form, mirroring
// the deleted hash-pair ledger's "both forms reviewed independently" contract
// (spec §5.2). A later change to either exposed form's bytes stops that
// signature from verifying and returns the item to pending; no separate
// bookkeeping is needed for that property, it falls out of signing bytes.
//
// The countersigning key comes from resolveSignerOrUnsigned: an injected
// Signer, else ssh-agent, else (user store only) the unsigned degraded path.
// Alongside the store write it snapshots the approved bytes (content kinds
// only, best-effort) so a later upstream change can be reviewed as a diff.
func SetItemTrust(cfg *config.Config, req SetItemTrustRequest) (*SetItemTrustResult, error) {
	cat, tRef, key, err := resolveMutationTarget(cfg, req.Loader, req.Ref)
	if err != nil {
		return nil, err
	}
	attestations, _, err := itemAttestations(cat, tRef, key)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %q to approve it: %w", req.Ref, err)
	}

	store, storeName, err := resolveCountersignStore(cfg, req.FS, req.Project, req.UserStore, req.ProjectStore)
	if err != nil {
		return nil, err
	}
	signer, unsigned, err := resolveDecisionSigner(cfg, req.Signer, req.Project, req.Root, signing.AssertionApprove)
	if err != nil {
		return nil, err
	}

	refStr, err := countersign.CountersignRef(tRef)
	if err != nil {
		return nil, fmt.Errorf("cannot approve %q: %w", req.Ref, err)
	}

	var principal string
	if !unsigned {
		principal = ssh.FingerprintSHA256(signer.PublicKey())
	}
	writeApprove := func(a itemAttestation) error {
		if unsigned {
			if err := store.WriteUnsignedApprove(refStr, a.Attested, a.Payload); err != nil {
				return err
			}
		} else if err := store.WriteApprove(refStr, a.Attested, a.Payload, signer); err != nil {
			return err
		}
		// Best-effort: the sidecar index is untrusted display metadata (spec
		// §9.2) that lets `ctxloom review` label a later pending item UPDATE
		// vs NEW and pick a diff base — never an input to any trust decision.
		// It records the LIVE kind and the LAYOUT form, not the attestation
		// form: those labels must stay readable across a contract bump, which
		// is what makes a superseded approval show up as an update rather than
		// as a first-time item.
		_ = store.AppendIndex(countersign.IndexEntry{
			Ref: refStr, Kind: string(tRef.Kind), Form: string(a.Layout), Assertion: string(signing.AssertionApprove),
			Principal: principal, Unsigned: unsigned, PayloadHash: bundles.HashPayload(a.Payload), ReviewedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}
	var rawHash, distilledHash string
	for _, a := range attestations {
		if err := writeApprove(a); err != nil {
			return nil, fmt.Errorf("countersign %q (%s): %w", req.Ref, a.Attested, err)
		}
		switch a.Layout {
		case signing.FormRaw:
			rawHash = bundles.HashPayload(a.Payload)
		case signing.FormDistilled:
			distilledHash = bundles.HashPayload(a.Payload)
		}
	}

	snapshotAcceptedItemContent(cfg, cat, tRef, key, req.FS, rawHash, distilledHash)

	res := &SetItemTrustResult{
		Status:   "approved",
		Ref:      tRef.Key(),
		RepoURL:  tRef.CanonicalURL(),
		Unsigned: unsigned,
		Store:    storeName,
	}
	if !unsigned {
		// Same fingerprint already computed into principal above.
		res.KeyFingerprint = principal
	}
	return res, nil
}

// SetBlacklistRequest rejects an item.
type SetBlacklistRequest struct {
	Ref string

	// Project writes to the COMMITTABLE project store instead of the
	// personal user store. See SetItemTrustRequest.Project.
	Project bool

	// Signer overrides key resolution (test injection).
	Signer ssh.Signer `json:"-"`

	UserStore    *countersign.Store `json:"-"`
	ProjectStore *countersign.Store `json:"-"`

	// Root overrides the trust root the signing key is authorized against.
	// See SetItemTrustRequest.Root — a rejection recorded with a key untrusted
	// for the REJECT namespace is the same silent no-op, one direction worse:
	// the user is told content is blocked when it is not.
	Root trust.TrustRoot `json:"-"`

	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetBlacklistResult reports the recorded rejection.
type SetBlacklistResult struct {
	Status  string `json:"status"` // "rejected"
	Ref     string `json:"ref"`
	RepoURL string `json:"repo_url"`
	// ContentForms are the forms a content-reject countersignature was
	// written for (raw and/or distilled); empty if the item could not be
	// resolved (the ref-level block is still written regardless).
	ContentForms   []string `json:"content_forms,omitempty"`
	KeyFingerprint string   `json:"key_fingerprint,omitempty"`
	Unsigned       bool     `json:"unsigned,omitempty"`
	Store          string   `json:"store"`
}

// SetBlacklist records BOTH companion components of a rejection, each as a
// countersignature (spec §5.3): a REF-scoped, form-agnostic "ref-reject" (the
// sticky block — denies this ref regardless of content/version, surviving
// changes), written even when the item cannot be resolved, and a
// REF-OMITTED "content-reject" per form the item currently has (raw, and
// distilled when present) — so a renamed/moved identical copy stays
// rejected wherever it appears (spec §5.3's asymmetry: approve binds the
// ref, reject's content component deliberately does not).
func SetBlacklist(cfg *config.Config, req SetBlacklistRequest) (*SetBlacklistResult, error) {
	cat, tRef, key, err := resolveMutationTarget(cfg, req.Loader, req.Ref)
	if err != nil {
		return nil, err
	}

	store, storeName, err := resolveCountersignStore(cfg, req.FS, req.Project, req.UserStore, req.ProjectStore)
	if err != nil {
		return nil, err
	}
	signer, unsigned, err := resolveDecisionSigner(cfg, req.Signer, req.Project, req.Root, signing.AssertionReject)
	if err != nil {
		return nil, err
	}

	refStr, err := countersign.CountersignRef(tRef)
	if err != nil {
		return nil, fmt.Errorf("cannot reject %q: %w", req.Ref, err)
	}

	// Ref-level (sticky) block — the durable guarantee, written even when the
	// item cannot be resolved (e.g. already deleted).
	if unsigned {
		if err := store.WriteUnsignedRefReject(refStr); err != nil {
			return nil, fmt.Errorf("countersign rejection of %q: %w", req.Ref, err)
		}
	} else {
		if err := store.WriteRefReject(refStr, signer); err != nil {
			return nil, fmt.Errorf("countersign rejection of %q: %w", req.Ref, err)
		}
	}

	// Best-effort content component: rejecting must succeed even when the
	// content is gone. The reported forms stay the LAYOUT names the user's
	// mental model uses ("raw", "distilled"); the record itself binds the
	// derived attestation form.
	var forms []string
	if attestations, _, herr := itemAttestations(cat, tRef, key); herr == nil {
		for _, a := range attestations {
			var werr error
			if unsigned {
				werr = store.WriteUnsignedContentReject(a.Attested, a.Payload)
			} else {
				werr = store.WriteContentReject(a.Attested, a.Payload, signer)
			}
			if werr == nil {
				forms = append(forms, string(a.Layout))
			}
		}
	}

	res := &SetBlacklistResult{
		Status:       "rejected",
		Ref:          tRef.Key(),
		RepoURL:      tRef.CanonicalURL(),
		ContentForms: forms,
		Unsigned:     unsigned,
		Store:        storeName,
	}
	if !unsigned {
		res.KeyFingerprint = ssh.FingerprintSHA256(signer.PublicKey())
	}
	return res, nil
}

// --- Ref parsing + hashing helpers -------------------------------------------

// computeItemPayloadPair loads the bundle and returns the item's (raw,
// distilled) PAYLOAD BYTES — the exact preimages the decision function and any
// signature both key on. rawPayload always covers the raw authored bytes (or the
// canonical executable surface for mcp/hooks, which have no distilled form);
// distilledPayload covers the distilled rewrite and is nil when no distilled
// form exists (or distillation is suppressed via NoDistill).
//
// Bytes are the primitive and hashes are derived from them, never the other
// way round: there is exactly ONE definition of "the bytes of item X in form
// F" in this codebase — bundles.ContentPayload — and everything that needs
// those bytes, or a hash of them, or a signature over them, comes through
// here. Two definitions is the bug.
//
// It also returns the bundle's VERIFIED publisher identity (empty for unsigned),
// so a caller resolving an item by ref gets the same signer the exposure gate
// would see.
//
// It returns BYTES BY LAYOUT FORM and deliberately not by role: callers that
// record a countersignature must go through itemAttestations, which pairs each
// payload with the attestation form derived from the item's kind. Nothing that
// writes a countersignature may pick a form itself.
func computeItemPayloadPair(cat bundles.Catalog, tRef trust.Ref, key trust.BundleKey) (rawPayload, distilledPayload []byte, signer string, err error) {
	bundle, err := cat.LoadKey(key)
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
			return nil, nil, "", fmt.Errorf("fragment %q not found in bundle %q", tRef.Name, key)
		}
		rawPayload, distilledPayload = payloadPair(frag.ContentPayload)
		return rawPayload, distilledPayload, signer, nil
	case trust.KindPrompt:
		prompt, ok := bundle.Commands[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("prompt %q not found in bundle %q", tRef.Name, key)
		}
		rawPayload, distilledPayload = payloadPair(prompt.ContentPayload)
		return rawPayload, distilledPayload, signer, nil
	case trust.KindMCP:
		mcp, ok := bundle.MCP[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("mcp server %q not found in bundle %q", tRef.Name, key)
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
			return nil, nil, "", fmt.Errorf("hook %q not found in bundle %q", tRef.Name, key)
		}
		payload, perr := entry.Hook.ContentPayload()
		if perr != nil {
			return nil, nil, "", fmt.Errorf("hook %q payload: %w", tRef.Name, perr)
		}
		return payload, nil, signer, nil
	case trust.KindSkill:
		skill, ok := bundle.Skills[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("skill %q not found in bundle %q", tRef.Name, key)
		}
		// A skill has no distilled form (SKILL.md's description IS the
		// progressive-disclosure mechanism; distilling it would defeat that) —
		// one payload, mirroring KindMCP/KindHook.
		// FSDir, not filepath.Dir(bundle.Path): this is a TRUST decision, and
		// the overloaded Path resolves a companion/seeded bundle to "." — the
		// process working directory — so the bytes hashed into the grant would
		// be whatever happened to sit there.
		skillDir, dirErr := bundle.SkillPreimageDir(skill)
		if dirErr != nil {
			return nil, nil, "", fmt.Errorf("skill %q: %w", tRef.Name, dirErr)
		}
		payload, perr := skill.ContentPayload(cat.FS(), skillDir, tRef.Name)
		if perr != nil {
			return nil, nil, "", fmt.Errorf("skill %q payload: %w", tRef.Name, perr)
		}
		return payload, nil, signer, nil
	default:
		return nil, nil, "", fmt.Errorf("unknown item kind %q", tRef.Kind)
	}
}

// itemAttestation is one countersignable materialization of an item: the exact
// payload bytes, the ATTESTATION form a countersignature over them binds, and
// the LAYOUT form the display-only sidecar index is keyed on.
//
// The two forms travel together because they answer different questions and are
// needed in the same breath — what the signature covers, and what a later review
// looks up to say "you approved something here once". Nothing constructs one of
// these by hand.
type itemAttestation struct {
	Attested signing.AttestationForm
	Layout   signing.Form
	Payload  []byte
}

// itemAttestations resolves every countersignable materialization of an item:
// its payload bytes (computeItemPayloadPair — the single definition of "the
// bytes of item X in form F") each paired with the attestation form derived from
// the item's KIND (countersign.AttestationFormFor). It is the role-aware entry point both
// write paths use, so an approval or a rejection can never be recorded under a
// kind-blind form.
//
// A kind with no attestation form yields an error rather than an empty list: it
// cannot be countersigned, and a caller told "nothing to record" would report a
// decision it never made.
func itemAttestations(cat bundles.Catalog, tRef trust.Ref, key trust.BundleKey) ([]itemAttestation, string, error) {
	rawPayload, distilledPayload, signer, err := computeItemPayloadPair(cat, tRef, key)
	if err != nil {
		return nil, "", err
	}
	out := make([]itemAttestation, 0, 2)
	for _, m := range []struct {
		layout  signing.Form
		payload []byte
	}{
		{signing.FormRaw, rawPayload},
		{signing.FormDistilled, distilledPayload},
	} {
		if len(m.payload) == 0 {
			continue
		}
		attested, ferr := countersign.AttestationFormFor(tRef.Kind, m.layout)
		if ferr != nil {
			return nil, "", ferr
		}
		out = append(out, itemAttestation{Attested: attested, Layout: m.layout, Payload: m.payload})
	}
	if len(out) == 0 {
		// An item with no bytes in any form has nothing to countersign, and
		// returning an empty list would let a caller report "approved" having
		// written no record at all — exit 0, a success message, zero bytes.
		// The store refuses an empty-payload approve for the same reason; this
		// is that refusal reaching the case where there is no payload to offer
		// it in the first place.
		return nil, "", fmt.Errorf("%s has no content in any form: there is nothing to countersign", tRef.Key())
	}
	return out, signer, nil
}

// computeItemPayload resolves the item's CURRENT effective form — distilled when
// cfg prefers it and a distilled form exists, else raw — returning the exact
// bytes assembly would expose, their form, and the bundle's verified signer.
func computeItemPayload(cfg *config.Config, cat bundles.Catalog, tRef trust.Ref, key trust.BundleKey) ([]byte, bundles.ContentForm, string, error) {
	rawPayload, distilledPayload, signer, err := computeItemPayloadPair(cat, tRef, key)
	if err != nil {
		return nil, "", "", err
	}
	if cfgPreferDistilled(cfg) && distilledPayload != nil {
		return distilledPayload, bundles.FormDistilled, signer, nil
	}
	return rawPayload, bundles.FormRaw, signer, nil
}

// --- list-JSON stamping -------------------------------------------------------

// TrustStamper resolves effective per-item trust for a single listing, building
// the review-records backing store, remote registry, and bundle loader ONCE and
// reusing them across every item it stamps. This is the listing cost control:
// the decision function is content-keyed, so a naive stamp would re-read the
// countersignature stores / remotes.yaml and re-materialize each item per call;
// the stamper reads the stores once and lets the shared loader cache each
// bundle after its first materialization.
//
// It is read-only and fault-tolerant by construction: a build failure or any
// per-item parse/resolve/hash failure never surfaces as an error — it stamps a
// fail-closed DENY (never "trusted"), so a listing can never crash and a hash
// failure can never produce a trusted stamp. Not safe for concurrent use.
type TrustStamper struct {
	cfg     *config.Config
	loader  *bundles.Loader
	gate    bundles.Authorizer
	records composite.ReviewRecords
	fs      afero.Fs
}

// TrustStamperOption injects a pre-built dependency, mirroring the loader
// option style. Tests drive the stamper over an in-memory store/loader;
// production stamps with the generation's own gate.
type TrustStamperOption func(*TrustStamper)

// WithStampRecords stamps with a gate built over r instead of the
// generation's: the review records a caller just wrote (a mutation that
// reports the decision it recorded), or a fixture over an in-memory fs.
func WithStampRecords(r composite.ReviewRecords) TrustStamperOption {
	return func(ts *TrustStamper) { ts.records = r }
}

// WithStampLoader injects a pre-built bundle loader (it must resolve the same
// refs the listing produced).
func WithStampLoader(l *bundles.Loader) TrustStamperOption {
	return func(ts *TrustStamper) { ts.loader = l }
}

// WithStampFS injects the filesystem the lockfile retraction is read from
// when WithStampRecords builds its own gate.
func WithStampFS(fs afero.Fs) TrustStamperOption {
	return func(ts *TrustStamper) { ts.fs = fs }
}

// NewTrustStamper builds a stamper for cfg. It never errors: it decides with
// the generation's Trust (cfg.ExecutableTrustGate), and a Config nobody bound
// stamps every item DENY (bundles.Decide's nil-gate withhold), never trusted.
func NewTrustStamper(cfg *config.Config, opts ...TrustStamperOption) *TrustStamper {
	ts := &TrustStamper{cfg: cfg}
	if cfg != nil {
		ts.gate = cfg.ExecutableTrustGate()
		ts.fs = cfg.FS()
	}
	for _, o := range opts {
		o(ts)
	}
	if ts.records != nil {
		ts.gate = trustOverRecords(cfg, ts.records, ts.fs).Authorizer()
	}
	if ts.loader == nil && cfg != nil {
		ts.loader = bundleLoader(cfg)
	}
	return ts
}

// trustOverRecords is the ONE gate shape an operation builds when it must
// decide with review records other than the generation's — the ones it just
// wrote, or a caller's injected stores: the generation's trust root and
// lockfile, over r.
func trustOverRecords(cfg *config.Config, r composite.ReviewRecords, fs afero.Fs) composite.Trust {
	root := reviewTrustRoot(cfg, nil)
	retraction := remote.NewLockfileRetraction(remote.NewLockfileManager(getBaseDir(cfg), remote.WithLockfileFS(getFS(fs))))
	tr, err := composite.NewTrust(root, r, retraction)
	if err != nil {
		panic(err) // every port is supplied above
	}
	return tr
}

// ForRef stamps a fragment/prompt/mcp item addressed by its full list ref
// "<source>#<kind>/<name>". It materializes the item's effective content
// through the shared loader (cached per bundle) to get the exact payload bytes
// + form + verified signer the decision function keys on, honoring
// ShouldUseDistilled. A parse/resolve failure stamps a fail-closed DENY
// (SourcePending): never trusted, never an error (fault tolerance +
// fail-closed for the trust signal).
//
// The trust identity is minted from the READ's own typed source, exactly as
// ForHook mints one: source is only how the item was ASKED for — a listing row
// carries the name the bundle answers to, which for a pinned bundle is its
// lockfile spelling — while the read is WHERE the bundle was actually found.
// Only the read's own answer is safe to key trust on.
func (ts *TrustStamper) ForRef(ref string) EffectiveTrustResult {
	pending := EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	ask, err := bundles.ParseItemAsk(ref)
	if err != nil || !ask.Scoped {
		return pending
	}
	read := ts.readAsk(ask.Bundle)
	br, err := read.SourceRef().WithItem(ask.Kind, ask.Item)
	if err != nil {
		return pending
	}
	tRef := trust.RefFromBundleRef(br)
	payload, form, _, err := computeItemPayload(ts.cfg, ts.loader.Catalog(), tRef, br.BundleIdentity())
	if err != nil {
		return pending
	}
	return ts.resolve(tRef, read, payload, form)
}

// ForHook stamps a bundle hook addressed by its (source, HookEntry) identity,
// mirroring the exec choke. It resolves the hook's executable surface
// (BundleHook.ContentPayload) through the decision function.
//
// source is the bundle ASK — the same local-or-canonical name printBundleHookTrust
// resolves any other bundle item by — and is used, unparsed, to look the read
// up through the shared (cached) loader. The trust identity itself is minted
// from that READ's own typed source (BundleRead.SourceRef, through the
// canonical bundle-reference grammar's item selector), never by reparsing
// source: the read is WHERE the bundle was actually found, which is honest by
// construction, while source is only how it was asked for — the two can
// diverge (an alias, a short name) and only the read's own answer is safe to
// key trust on. This mirrors reviewEnumerator.classify's identical fix.
//
// The bundle's verified SIGNER is resolved through the same shared loader —
// because the stamp must agree with the gate. config.extractHooksFromBundle
// gates this same hook with the bundle's signer in hand; a stamp that omitted
// it would report "pending" for a hook the gate actually exposes, which is a
// lie in a security display.
func (ts *TrustStamper) ForHook(source string, entry bundles.HookEntry) EffectiveTrustResult {
	payload, perr := entry.Hook.ContentPayload()
	if perr != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	read := ts.readAsk(source)
	br, err := read.SourceRef().WithItem(trust.KindHook, entry.ID())
	if err != nil {
		// The read's source could not be addressed (unresolved bundle, or a
		// mint that failed) — nothing to stamp trust against.
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	tRef := trust.RefFromBundleRef(br)
	return ts.resolve(tRef, read, payload, bundles.FormRaw)
}

// readAsk returns the READ of the bundle a listing ASKED for by name — the
// trust facts its reader established, which is what the decision's first-party
// step keys on. An unresolvable bundle yields an UNCLAIMED read, which reaches
// no exemption arm and falls out the fail-closed default: MORE review, never
// more exposure, matching signerFor's own fail-safe.
func (ts *TrustStamper) readAsk(source string) bundles.BundleRead {
	if ts.loader == nil || source == "" {
		return bundles.BundleRead{}
	}
	read, err := ts.loader.Read(source)
	if err != nil {
		return bundles.BundleRead{}
	}
	return read
}

// resolve runs the decision function with the stamper's shared records store,
// so no item re-reads the countersignature stores. It does NOT make the call
// I/O-free: Retraction is left unset, so EffectiveTrust reads and parses the
// active lockfile once per stamped item (measured in
// trust_perkitem_io_test.go). Sharing that too would fix the sample point of
// retraction state for a whole listing, which is a trust decision, not a
// caching one.
func (ts *TrustStamper) resolve(ref trust.Ref, read bundles.BundleRead, payload []byte, form bundles.ContentForm) EffectiveTrustResult {
	if ts.gate == nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	v := ts.gate.Admit(bundles.Exposure{Read: read, Ref: ref, RefStr: ref.Key(), Bytes: payload, Form: form})
	return resultOf(v)
}

// resultOf projects a gate's Verdict onto the stamped result: the Source is
// the cascade STEP the Reason names, and Detail travels as the verdict's.
func resultOf(v bundles.Verdict) EffectiveTrustResult {
	res := EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending, Detail: v.Detail}
	if v.Allow {
		res.Decision = trust.Allow
	}
	switch v.Reason {
	case bundles.ReasonRejected:
		res.Source = trust.SourceRejected
	case bundles.ReasonRetracted:
		res.Source = trust.SourceRetracted
	case bundles.ReasonLocal, bundles.ReasonStaleLocalSignature:
		res.Source = trust.SourceLocal
	case bundles.ReasonBuiltin:
		res.Source = trust.SourceBuiltin
	case bundles.ReasonCompanion:
		res.Source = trust.SourceCompanion
	case bundles.ReasonTrustedSigner:
		res.Source = trust.SourceTrustedSigner
	case bundles.ReasonApproved:
		res.Source = trust.SourceAccepted
	case bundles.ReasonUngated:
		res.Source = trust.SourceLocal
	}
	return res
}
