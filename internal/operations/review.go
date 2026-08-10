package operations

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Review enumeration (trust-simplify slice 2): the operations core under the
// `ctxloom review` porcelain. It walks every bundle the management loader can
// see — lockfile-visible remote bundles (seeded from the clone cache) plus
// fs-installed local-cache bundles — through the UNGATED loader, resolves each
// item through the slice-1 decision function, and returns exactly the items a
// human still needs to review: pending (never reviewed, or changed since
// acceptance). First-party items (local, trusted source) and already-decided
// items (accepted at the current hash, rejected) never appear.

// Review item statuses. An item whose ref has an accepted state but whose
// current hash no longer matches is an UPDATE (a human saw an earlier version);
// everything else pending is NEW.
const (
	ReviewStatusNew    = "new"
	ReviewStatusUpdate = "update"
)

// ReviewItem is one pending item awaiting human review.
type ReviewItem struct {
	Bundle string `json:"bundle"` // the bundle's source ref (canonical for remote, name for local)
	Ref    string `json:"ref"`    // full item ref, directly usable by trust/blacklist
	Kind   string `json:"kind"`   // display selector dir: fragments|commands|mcp|hooks|skills
	Name   string `json:"name"`   // item name (hooks: "<event>/<index>")
	Status string `json:"status"` // ReviewStatusNew | ReviewStatusUpdate

	// Executable marks the render-what-it-runs kinds (mcp, hooks).
	Executable bool `json:"-"`
	// CurrentContent is what review shows: the effective content that would be
	// exposed (fragments/commands) or the rendered executable surface (mcp/hooks).
	CurrentContent string `json:"-"`
	// PreviousContent is the snapshot of the previously-accepted effective form
	// for an UPDATE — the diff base. Empty when no snapshot exists (e.g. a
	// migrated v1 grant): review then falls back to full-content display.
	PreviousContent string `json:"-"`

	// CurrentForm names the form CurrentContent is in ("raw"|"distilled"),
	// empty for the single-form kinds (mcp/hooks/skills).
	CurrentForm string `json:"-"`
	// AlternateContent is the OTHER form of a distillable item — the bytes an
	// approval also countersigns (SetItemTrust signs raw AND distilled) but
	// which are not the currently-exposed form. Review MUST show it: without
	// it, approving a fragment you read in one form silently blesses bytes you
	// never saw, and flipping use_distilled then serves them without
	// re-gating. Empty when the item has only one form.
	AlternateContent string `json:"-"`
	// AlternateForm names AlternateContent's form ("raw"|"distilled").
	AlternateForm string `json:"-"`
}

// Review reports WHO signed a bundle through bundles.Reason, the same
// vocabulary the exposure gate decides in — so a signature that failed to
// verify reads as INVALID rather than as indistinguishable from no signature
// at all, the exact state a reviewer most needs named. See bundles.PublisherOf.
//
// ReasonUnsigned and ReasonUntrustedSigner stay separate reasons rather than
// collapsing into one "pending": "pending" and "pending because I do not
// trust who signed it" are different diagnoses with different fixes
// (`ctxloom review` vs `signer trust`), even though the exposure gate
// — correctly — treats them identically for admission.

// ReviewBundle groups a bundle's pending items for the per-bundle walk.
type ReviewBundle struct {
	Ref    string       `json:"ref"`              // bundle source ref
	Remote string       `json:"remote,omitempty"` // registered remote name, when resolvable
	Items  []ReviewItem `json:"items"`

	// Publisher is the bundle's signer state — see bundles.PublisherOf.
	Publisher bundles.Reason `json:"publisher"`
	// Signer is the VERIFIED publisher principal, resolved from the trust root
	// (bundles.Bundle.Signer). Set only for bundles.ReasonTrustedSigner, where
	// it is a real identity.
	Signer string `json:"signer,omitempty"`
	// SignerFingerprint is the DISPLAY-ONLY fingerprint of the key that made an
	// untrusted signature (bundles.BundleRead.UntrustedSignerFingerprint). Set
	// only for bundles.ReasonUntrustedSigner.
	//
	// It is a string to COMPARE against what the publisher stated out of band,
	// and nothing else. It is not a name, not an endorsement, and not usable as
	// an input to any decision; whatever renders it must say the key is not
	// trusted in the same breath.
	SignerFingerprint string `json:"signer_fingerprint,omitempty"`
}

// PendingReviewRequest carries the optional injection points (for testing).
type PendingReviewRequest struct {
	// UserStore / ProjectStore override the physical countersignature stores
	// (test injection); production builds them from cfg. See
	// buildCountersignRecords.
	UserStore    *countersign.Store `json:"-"`
	ProjectStore *countersign.Store `json:"-"`
	// Root overrides the allowed_signers trust root (test injection);
	// production uses cfg.TrustRoot(). Every candidate countersignature must
	// clear this namespace/role check before it counts.
	Root     signing.TrustRoot `json:"-"`
	Registry *remote.Registry  `json:"-"`
	Loader   *bundles.Loader   `json:"-"`
	FS       afero.Fs          `json:"-"`
}

// PendingReviewResult is the pending-review enumeration, grouped by bundle in
// deterministic order.
type PendingReviewResult struct {
	Bundles []ReviewBundle `json:"bundles"`
	Total   int            `json:"total"`
	Updates int            `json:"updates"`
}

// PendingReview enumerates every item awaiting review. It builds the
// review-records store (the union of the user and project countersignature
// stores), remote registry, and bundle loader ONCE (like TrustStamper) and
// resolves each item through EffectiveTrust with them injected, so the walk
// costs one store read + one registry read + one materialization per bundle.
//
// Unlike the deleted hash-pair trust.yaml, there is no single file whose
// corruption can deny the whole walk — the countersignature stores degrade to
// "no candidates" per item, never to a load error. An unreadable registry only
// warns: the walk then treats trusted sources as pending, and reviewing a
// first-party item merely records a harmless approval.
func PendingReview(cfg *config.Config, req PendingReviewRequest) (*PendingReviewResult, error) {
	records := buildCountersignRecords(cfg, req.FS, req.UserStore, req.ProjectStore, req.Root)
	// The registry is DISPLAY ONLY here — it resolves a bundle's canonical URL to
	// the remote name shown in the review header. It has no say in any trust
	// decision: trust is keyed to the signing identity, never to the remote the
	// bytes arrived from. An unreadable registry costs a label, nothing more.
	registry := req.Registry
	if registry == nil {
		if reg, rerr := getRegistry(cfg, remote.WithRegistryFS(getFS(req.FS))); rerr == nil {
			registry = reg
		} else {
			clidiag.Warn("ctxloom", "remote registry unreadable; review will show canonical refs instead of remote names: %v", rerr)
		}
	}
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	// Iterate the READS, not a listing plus a path round trip: a bundle that
	// has no file — a companion loadout, a pinned document — has no path to
	// resolve back through, and asking for one dropped exactly that content
	// from review with a "bundle not found" nobody could act on.
	e := &reviewEnumerator{cfg: cfg, records: records, fs: req.FS,
		authorizer: &contentGate{cfg: cfg, records: records, fs: req.FS}}
	result := &PendingReviewResult{}
	for _, read := range loader.Reads() {
		ref := read.Ref()
		items := e.pendingItems(ref, read)
		if len(items) == 0 {
			continue
		}
		publisher, principal, fingerprint := reviewPublisherOf(read)
		result.Bundles = append(result.Bundles, ReviewBundle{
			Ref:               ref,
			Remote:            remoteNameFor(registry, ref),
			Items:             items,
			Publisher:         publisher,
			Signer:            principal,
			SignerFingerprint: fingerprint,
		})
		result.Total += len(items)
		for _, it := range items {
			if it.Status == ReviewStatusUpdate {
				result.Updates++
			}
		}
	}
	// loader.List() is name-sorted already; keep the guarantee explicit.
	sort.Slice(result.Bundles, func(i, j int) bool { return result.Bundles[i].Ref < result.Bundles[j].Ref })
	return result, nil
}

// reviewPublisherOf reports what a bundle's READ says about its publisher, plus
// whichever of the principal / fingerprint belongs to that state.
//
// It DERIVES, and never decides: every value comes from the reader that already
// asked the trust root, and nothing here can promote any of them. The
// fingerprint half is returned only for the untrusted state, where the renderer
// is obliged to say the key is not trusted — it is never returned as, or
// alongside, an identity.
//
// trust.BuiltinSigner never appears as a principal here for exactly the reason
// EffectiveTrust's trusted-signer step excludes it: it is a SYNTHETIC identity,
// not a cryptographic one, and reporting it as a trusted publisher key would
// launder "shipped inside this binary" into "a publisher you verified". A
// builtin reads as unsigned, which is what it is; it is allowed as a builtin and
// never appears in this listing anyway.
func reviewPublisherOf(read bundles.BundleRead) (state bundles.Reason, principal, fingerprint string) {
	state = bundles.PublisherOf(read)
	switch state {
	case bundles.ReasonTrustedSigner:
		if signer := read.Bundle.Signer(); signer != trust.BuiltinSigner {
			return state, signer, ""
		}
		return bundles.ReasonUnsigned, "", ""
	case bundles.ReasonUntrustedSigner:
		return state, "", read.UntrustedSignerFingerprint()
	default:
		return state, "", ""
	}
}

// reviewEnumerator resolves items against the shared records/registry.
type reviewEnumerator struct {
	cfg     *config.Config
	records countersignRecords
	fs      afero.Fs
	// authorizer is the SAME decision the exposure path uses, built once over
	// the shared records store. Review asks it what would be delivered rather
	// than re-deriving an opinion of its own.
	authorizer bundles.Authorizer
}

// pendingItems walks one bundle's items in stable display order — fragments,
// commands, MCP servers, hooks; name-sorted within each kind — and returns the
// pending ones.
func (e *reviewEnumerator) pendingItems(bundleRef string, read bundles.BundleRead) []ReviewItem {
	var out []ReviewItem
	bundle := read.Bundle
	preferDistilled := cfgPreferDistilled(e.cfg)

	// The bundle's READ is carried into every item's decision, so review resolves
	// each item through the SAME Authorizer the exposure path does and shows exactly
	// what would be delivered: an item from a trusted publisher is NOT pending
	// and must not be presented for review as though it were, and a TAMPERED
	// bundle's items are not "unsigned content awaiting a look" either.

	for _, name := range bundle.FragmentNames() {
		frag := bundle.Fragments[name]
		payload, form := frag.ContentPayload(preferDistilled)
		item, ok := e.classify(bundleRef, "fragments", name, read, payload, string(form), false)
		if !ok {
			continue
		}
		setReviewForms(&item, payload, form, frag.ContentPayload)
		out = append(out, item)
	}
	for _, name := range bundle.PromptNames() {
		command := bundle.Commands[name]
		payload, form := command.ContentPayload(preferDistilled)
		item, ok := e.classify(bundleRef, "commands", name, read, payload, string(form), false)
		if !ok {
			continue
		}
		setReviewForms(&item, payload, form, command.ContentPayload)
		out = append(out, item)
	}
	for _, name := range bundle.MCPNames() {
		srv := bundle.MCP[name]
		payload, perr := srv.ContentPayload()
		if perr != nil {
			// Mirror classify's warning for a structurally
			// identical "cannot address this item" case — the gate withholds
			// it anyway, so review is the only place the user could learn
			// why an item they can see in the bundle never appears.
			clidiag.Warn("ctxloom", "review: skipping mcp %q in bundle %q: %v", name, bundleRef, perr)
			continue
		}
		item, ok := e.classify(bundleRef, "mcp", name, read, payload, string(bundles.FormRaw), true)
		if !ok {
			continue
		}
		item.CurrentContent = renderMCPSurface(srv)
		out = append(out, item)
	}
	for _, entry := range bundle.Hooks.Entries() {
		payload, perr := entry.Hook.ContentPayload()
		if perr != nil {
			clidiag.Warn("ctxloom", "review: skipping hook %q in bundle %q: %v", entry.ID(), bundleRef, perr)
			continue
		}
		item, ok := e.classify(bundleRef, "hooks", entry.ID(), read, payload, string(bundles.FormRaw), true)
		if !ok {
			continue
		}
		item.CurrentContent = renderHookSurface(entry)
		out = append(out, item)
	}
	for _, name := range bundle.SkillNames() {
		skill := bundle.Skills[name]
		// Bundle.Path is overloaded and filepath.Dir of a companion/seeded
		// value is ".", which would hash files from the process working
		// directory into what the user is asked to approve. Only a
		// manifest-LESS skill actually needs the tree, so only that case is
		// fatal here — see SkillPreimageDir.
		skillDir, dirErr := bundle.SkillPreimageDir(skill)
		if dirErr != nil {
			clidiag.Warn("ctxloom", "bundle %q: cannot review skill %q: %v", bundleRef, name, dirErr)
			continue
		}
		// The EFFECTIVE manifest — authored if synced, derived from the tree
		// if not — is resolved once and used for BOTH the preimage the user
		// is asked to approve and the file listing they are shown. Rendering
		// the authored manifest while hashing a derived one would ask for
		// approval of files review never displayed.
		manifest, merr := skill.EffectiveManifest(e.fs, skillDir, name)
		if merr != nil {
			clidiag.Warn("ctxloom", "review: skipping skill %q in bundle %q: %v", name, bundleRef, merr)
			continue
		}
		payload, perr := skill.ContentPayload(e.fs, skillDir, name)
		if perr != nil {
			clidiag.Warn("ctxloom", "review: skipping skill %q in bundle %q: %v", name, bundleRef, perr)
			continue
		}
		// executable=false: unlike mcp/hooks, a skill IS a reviewable TREE —
		// that is the entire reason it is stored as a directory of files
		// rather than a blob. Marking it non-executable routes it through the
		// same content-snapshot path as fragments/commands (see
		// review_snapshots.go's itemContentPair), so editing any one file in
		// the tree — SKILL.md or a scripts/ script — shows up as a per-file
		// diff against what was previously accepted, not a bare "changed".
		item, ok := e.classify(bundleRef, "skills", name, read, payload, string(bundles.FormRaw), false)
		if !ok {
			continue
		}
		item.CurrentContent = renderSkillSurface(manifest)
		out = append(out, item)
	}
	return out
}

// setReviewForms fills a distillable item's displayed content AND the other
// form's content when one exists.
//
// SetItemTrust countersigns BOTH forms of a distillable item (raw always,
// distilled when present), so an approval covers bytes in both forms. Review
// must therefore present both — otherwise the human reads one form, blesses
// two, and a later use_distilled flip serves unread bytes with no re-gate.
// The pair is derived from the SAME preimage builder the
// countersignature uses (computeItemPayloadPair's payloadPair), so what the
// reviewer sees and what gets signed can never drift apart.
func setReviewForms(item *ReviewItem, shown []byte, shownForm bundles.ContentForm, payload func(bool) ([]byte, bundles.ContentForm)) {
	item.CurrentContent = string(shown)
	item.CurrentForm = string(shownForm)

	raw, _ := payload(false)
	distilled, distilledForm := payload(true)
	if distilledForm != bundles.FormDistilled {
		return // single-form item: nothing else is countersigned
	}
	if shownForm == bundles.FormDistilled {
		item.AlternateContent, item.AlternateForm = string(raw), string(bundles.FormRaw)
		return
	}
	item.AlternateContent, item.AlternateForm = string(distilled), string(bundles.FormDistilled)
}

// classify resolves one item through the decision function and, when it is
// pending, returns its ReviewItem shell (status + diff base resolved; content
// filled by the caller, which has the item in hand). ok=false means the item
// needs no review (allowed, rejected, or unaddressable).
func (e *reviewEnumerator) classify(bundleRef, kindDir, name string, read bundles.BundleRead, payload []byte, form string, executable bool) (ReviewItem, bool) {
	ref := bundleRef + "#" + kindDir + "/" + name
	tRef, _, _, err := trust.ParseItemRef(ref)
	if err != nil {
		// A ref review cannot address cannot be accepted either — the exposure
		// gate withholds it regardless; surface the anomaly and move on.
		clidiag.Warn("ctxloom", "review: skipping unaddressable item %q: %v", ref, err)
		return ReviewItem{}, false
	}
	// THE SAME FILTER THE EXPOSURE PATH USES, not a second opinion about the
	// same item. That is the whole point of the verdict: a status report is
	// truthful by CONSTRUCTION rather than because two code paths that both
	// re-derive a decision happen to agree. The old separate call into
	// EffectiveTrust could — and, for a tampered remote bundle, did — reach a
	// different answer than the one that decided delivery.
	//
	// NeedsReview is the predicate, not "denied": rejected and retracted are
	// decided, and TAMPERED is deliberately not reviewable. Listing tampered
	// bytes as pending is the spec §10.2 downgrade completing itself — the
	// content arrives in the queue looking like ordinary unsigned content and a
	// human approves it.
	v := e.authorizer.Admit(bundles.Exposure{
		Read:  read,
		Ref:   tRef,
		Bytes: payload,
		Form:  bundles.ContentForm(form),
	})
	bundles.ReportVerdict(ref, v)
	if !v.Reason.NeedsReview() {
		return ReviewItem{}, false
	}

	item := ReviewItem{
		Bundle:     bundleRef,
		Ref:        ref,
		Kind:       kindDir,
		Name:       name,
		Status:     ReviewStatusNew,
		Executable: executable,
	}
	// UPDATE detection + diff base: consult the (display-only, untrusted)
	// sidecar index for a PRIOR approve attempt at this ref+form — a human saw
	// an earlier version if one exists, even though it no longer verifies (that
	// is exactly why the item is pending again). This is never a trust decision,
	// only a label + a diff base; EffectiveTrust above has already,
	// independently, decided this item is pending.
	//
	// It is keyed on the LAYOUT form, which is what makes an approval superseded
	// by a countersign-contract bump read as an UPDATE rather than as a NEW item:
	// the record can no longer verify, but a human's earlier look at this ref is
	// still a fact, and telling them "new" would hide it.
	refStr := countersignRef(tRef)
	entry, found, idxErr := latestApproveEntry(e.records, refStr, signing.Form(form))
	if idxErr != nil {
		// "I cannot read the index" is not "there was never a prior
		// approval". Say so, and take the conservative label: UPDATE puts the
		// reviewer on notice that these bytes may be a substitution for
		// something they already looked at, which is the reading that costs
		// them a second look rather than a missed one. The diff base is
		// genuinely unavailable, so none is offered.
		clidiag.Warn("ctxloom", "review: cannot read the approvals index for %q, treating it as an update: %v", ref, idxErr)
		item.Status = ReviewStatusUpdate
		return item, true
	}
	if found {
		item.Status = ReviewStatusUpdate
		if !executable {
			if snap, ok := readTrustSnapshot(getFS(e.fs), getBaseDir(e.cfg), entry.PayloadHash); ok {
				item.PreviousContent = snap
			}
		}
	}
	return item, true
}

// latestApproveEntry looks up the most recent prior approve attempt across
// BOTH the user and project stores' sidecar indexes (display-only; see
// countersign.Store.LatestApprove).
// An unreadable index is reported, never folded into "no prior approval":
// that answer would relabel an UPDATE as NEW and hide the diff a reviewer
// looks at.
func latestApproveEntry(records countersignRecords, ref string, layout signing.Form) (countersign.IndexEntry, bool, error) {
	var latest countersign.IndexEntry
	found := false
	for _, st := range records.bothStores() {
		e, ok, err := st.LatestApprove(ref, layout)
		if err != nil {
			return countersign.IndexEntry{}, false, err
		}
		if ok {
			if !found || e.IsAfter(latest) {
				latest = e
				found = true
			}
		}
	}
	return latest, found, nil
}

// remoteNameFor resolves a bundle ref's source repo to its registered remote
// name for the review header ("" when unresolvable — the canonical ref is
// still shown). Both sides canonicalize through trust.CanonicalRepoURL.
func remoteNameFor(reg *remote.Registry, bundleRef string) string {
	if reg == nil {
		return ""
	}
	parsed, err := remote.ParseReference(bundleRef)
	if err != nil || parsed.IsLocal || parsed.URL == "" {
		return ""
	}
	canonical := trust.CanonicalRepoURL(parsed.URL)
	for _, rem := range reg.List() {
		if trust.CanonicalRepoURL(rem.URL) == canonical {
			return rem.Name
		}
	}
	return ""
}

// emptySurfaceMarker replaces a render*Surface function's output when the
// underlying item carries no executable content at all: a blank
// field (or, for renderSkillSurface, "") reads to a reviewer as "nothing
// changed here", not as "this item genuinely has nothing in it" — a subtle
// but real difference when the reviewer is about to bless it. Display-only:
// the countersignature still binds ContentPayload()'s real (possibly empty)
// bytes regardless of what review shows.
const emptySurfaceMarker = "(nothing to display — this item is empty)\n"

// renderMCPSurface renders an MCP server as what it runs — command, args, env
// (key-sorted), and the installation instructions sent to the agent. This is
// the full executable surface the acceptance hash covers; notes are
// human-only metadata and deliberately absent (they are not part of the
// reviewed surface).
func renderMCPSurface(srv bundles.BundleMCP) string {
	if srv.Command == "" && len(srv.Args) == 0 && len(srv.Env) == 0 && srv.Installation == "" {
		return emptySurfaceMarker
	}
	var b strings.Builder
	fmt.Fprintf(&b, "command: %s\n", srv.Command)
	if len(srv.Args) > 0 {
		fmt.Fprintf(&b, "args:    %s\n", strings.Join(srv.Args, " "))
	}
	if len(srv.Env) > 0 {
		keys := make([]string, 0, len(srv.Env))
		for k := range srv.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "env:     %s=%s\n", k, srv.Env[k])
		}
	}
	if srv.Installation != "" {
		fmt.Fprintf(&b, "install: %s\n", strings.TrimSpace(srv.Installation))
	}
	return b.String()
}

// renderHookSurface renders a bundle hook as what it runs and when it fires —
// event, matcher, type, command/prompt: the surface its acceptance hash covers
// (plus the event, which is carried by its identity).
func renderHookSurface(entry bundles.HookEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event:   %s\n", entry.Event)
	if entry.Hook.Matcher != "" {
		fmt.Fprintf(&b, "matcher: %s\n", entry.Hook.Matcher)
	}
	if entry.Hook.Type != "" {
		fmt.Fprintf(&b, "type:    %s\n", entry.Hook.Type)
	}
	if entry.Hook.Command != "" {
		fmt.Fprintf(&b, "command: %s\n", entry.Hook.Command)
	}
	if entry.Hook.Prompt != "" {
		fmt.Fprintf(&b, "prompt:  %s\n", strings.TrimSpace(entry.Hook.Prompt))
	}
	if entry.Hook.Command == "" && entry.Hook.Prompt == "" {
		// A hook with neither a command nor a prompt executes nothing — say
		// so explicitly rather than letting the event/matcher/type header
		// read as a complete, reviewed surface.
		b.WriteString(emptySurfaceMarker)
	}
	return b.String()
}

// renderSkillSurface renders a skill package as a per-file TREE listing —
// path, content hash, and POSIX mode, one line per file, sorted — rather than
// a single blob. This is what makes the package reviewable file-by-file: when
// review shows this as an UPDATE, the unified diff against the previous
// listing (readTrustSnapshot / unifiedReviewDiff) surfaces exactly which
// file(s) changed, added, or were removed, including a mode flip on a
// scripts/ entry (0644 -> 0755), not merely "the skill changed".
// It takes the resolved EFFECTIVE manifest rather than the entry so that what
// is displayed is exactly what the trust preimage covers, for a synced and an
// unsynced skill alike.
func renderSkillSurface(manifest bundles.SkillManifest) string {
	if len(manifest) == 0 {
		return emptySurfaceMarker
	}
	var b strings.Builder
	for _, entry := range manifest {
		fmt.Fprintf(&b, "%s  %s  mode:%s\n", entry.Path, entry.SHA256, entry.Mode)
	}
	return b.String()
}
