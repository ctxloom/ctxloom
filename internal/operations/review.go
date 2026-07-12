package operations

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

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
	Kind   string `json:"kind"`   // display selector dir: fragments|skills|mcp|hooks
	Name   string `json:"name"`   // item name (hooks: "<event>/<index>")
	Status string `json:"status"` // ReviewStatusNew | ReviewStatusUpdate

	// Executable marks the render-what-it-runs kinds (mcp, hooks).
	Executable bool `json:"-"`
	// CurrentContent is what review shows: the effective content that would be
	// exposed (fragments/skills) or the rendered executable surface (mcp/hooks).
	CurrentContent string `json:"-"`
	// PreviousContent is the snapshot of the previously-accepted effective form
	// for an UPDATE — the diff base. Empty when no snapshot exists (e.g. a
	// migrated v1 grant): review then falls back to full-content display.
	PreviousContent string `json:"-"`
}

// ReviewBundle groups a bundle's pending items for the per-bundle walk.
type ReviewBundle struct {
	Ref    string       `json:"ref"`              // bundle source ref
	Remote string       `json:"remote,omitempty"` // registered remote name, when resolvable
	Items  []ReviewItem `json:"items"`
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

	infos, err := loader.List()
	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}

	e := &reviewEnumerator{cfg: cfg, records: records, registry: registry, fs: req.FS}
	result := &PendingReviewResult{}
	for _, info := range infos {
		if info.Deleted {
			continue // gone upstream: nothing to show, nothing to accept
		}
		bundle, lerr := loader.LoadFile(info.Path)
		if lerr != nil {
			clidiag.Warn("ctxloom", "review: skipping bundle %q: %v", info.Name, lerr)
			continue
		}
		items := e.pendingItems(info.Name, bundle)
		if len(items) == 0 {
			continue
		}
		result.Bundles = append(result.Bundles, ReviewBundle{
			Ref:    info.Name,
			Remote: remoteNameFor(registry, info.Name),
			Items:  items,
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

// reviewEnumerator resolves items against the shared records/registry.
type reviewEnumerator struct {
	cfg      *config.Config
	records  countersignRecords
	registry *remote.Registry
	fs       afero.Fs
}

// pendingItems walks one bundle's items in stable display order — fragments,
// skills, MCP servers, hooks; name-sorted within each kind — and returns the
// pending ones.
func (e *reviewEnumerator) pendingItems(bundleRef string, bundle *bundles.Bundle) []ReviewItem {
	var out []ReviewItem
	preferDistilled := true
	if e.cfg != nil {
		preferDistilled = e.cfg.ShouldUseDistilled()
	}

	// The bundle's verified publisher identity, carried into every item's
	// decision so review shows exactly what the exposure gate would decide: an
	// item from a trusted publisher is NOT pending and must not be presented for
	// review as though it were.
	signer := bundle.Signer()

	for _, name := range bundle.FragmentNames() {
		frag := bundle.Fragments[name]
		payload, form := frag.ContentPayload(preferDistilled)
		item, ok := e.classify(bundleRef, "fragments", name, payload, string(form), signer, false)
		if !ok {
			continue
		}
		item.CurrentContent = string(payload)
		out = append(out, item)
	}
	for _, name := range bundle.PromptNames() {
		skill := bundle.Skills[name]
		payload, form := skill.ContentPayload(preferDistilled)
		item, ok := e.classify(bundleRef, "skills", name, payload, string(form), signer, false)
		if !ok {
			continue
		}
		item.CurrentContent = string(payload)
		out = append(out, item)
	}
	for _, name := range bundle.MCPNames() {
		srv := bundle.MCP[name]
		payload, perr := srv.ContentPayload()
		if perr != nil {
			continue
		}
		item, ok := e.classify(bundleRef, "mcp", name, payload, string(bundles.FormRaw), signer, true)
		if !ok {
			continue
		}
		item.CurrentContent = renderMCPSurface(srv)
		out = append(out, item)
	}
	for _, entry := range bundle.Hooks.Entries() {
		payload, perr := entry.Hook.ContentPayload()
		if perr != nil {
			continue
		}
		item, ok := e.classify(bundleRef, "hooks", entry.ID(), payload, string(bundles.FormRaw), signer, true)
		if !ok {
			continue
		}
		item.CurrentContent = renderHookSurface(entry)
		out = append(out, item)
	}
	return out
}

// classify resolves one item through the decision function and, when it is
// pending, returns its ReviewItem shell (status + diff base resolved; content
// filled by the caller, which has the item in hand). ok=false means the item
// needs no review (allowed, rejected, or unaddressable).
func (e *reviewEnumerator) classify(bundleRef, kindDir, name string, payload []byte, form, signer string, executable bool) (ReviewItem, bool) {
	ref := bundleRef + "#" + kindDir + "/" + name
	tRef, _, _, err := parseTrustItemRef(ref)
	if err != nil {
		// A ref review cannot address cannot be accepted either — the exposure
		// gate withholds it regardless; surface the anomaly and move on.
		clidiag.Warn("ctxloom", "review: skipping unaddressable item %q: %v", ref, err)
		return ReviewItem{}, false
	}
	res, err := EffectiveTrust(e.cfg, EffectiveTrustRequest{
		Ref:     tRef,
		Payload: payload,
		Form:    form,
		Signer:  signer,
		Records: e.records,
		FS:      e.fs,
	})
	if err != nil || res == nil || res.State() != trust.StatePending {
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
	// sidecar index for a PRIOR approve attempt at this ref+kind+form — a
	// human saw an earlier version if one exists, even though it no longer
	// verifies (that is exactly why the item is pending again). This is never
	// a trust decision, only a label + a diff base; EffectiveTrust above has
	// already, independently, decided this item is pending.
	signingKind := signingKindOf(tRef.Kind)
	refStr := countersignRef(tRef)
	entry, found := latestApproveEntry(e.records, signingKind, refStr, signing.Form(form))
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
func latestApproveEntry(records countersignRecords, kind signing.ItemKind, ref string, form signing.Form) (countersign.IndexEntry, bool) {
	var latest countersign.IndexEntry
	found := false
	for _, st := range records.bothStores() {
		if e, ok := st.LatestApprove(kind, ref, form); ok {
			if !found || e.ReviewedAt > latest.ReviewedAt {
				latest = e
				found = true
			}
		}
	}
	return latest, found
}

// remoteNameFor resolves a bundle ref's source repo to its registered remote
// name for the review header ("" when unresolvable — the canonical ref is
// still shown). Both sides canonicalize through trust.CanonicalRepoURL.
func remoteNameFor(reg *remote.Registry, bundleRef string) string {
	if reg == nil {
		return ""
	}
	parsed, err := remote.ParseReference(bundleRef)
	if err != nil || parsed.IsLocal || parsed.RepoURL() == "" {
		return ""
	}
	canonical := trust.CanonicalRepoURL(parsed.RepoURL())
	for _, rem := range reg.List() {
		if trust.CanonicalRepoURL(rem.URL) == canonical {
			return rem.Name
		}
	}
	return ""
}

// renderMCPSurface renders an MCP server as what it runs — command, args, env
// (key-sorted), and the installation instructions sent to the agent. This is
// the full executable surface the acceptance hash covers; notes are
// human-only metadata and deliberately absent (they are not part of the
// reviewed surface).
func renderMCPSurface(srv bundles.BundleMCP) string {
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
	return b.String()
}

// --- bundle-accept-all ---------------------------------------------------------

// AcceptReviewItemsRequest names the item refs to accept, plus test injection.
type AcceptReviewItemsRequest struct {
	Refs []string

	Project bool
	Signer  ssh.Signer `json:"-"`

	UserStore    *countersign.Store `json:"-"`
	ProjectStore *countersign.Store `json:"-"`
	Loader       *bundles.Loader    `json:"-"`
	FS           afero.Fs           `json:"-"`
}

// AcceptReviewItemsResult reports what was recorded. Failures are per-ref and
// never abort the batch (partial success is success — CLAUDE.md).
type AcceptReviewItemsResult struct {
	Accepted []string          `json:"accepted"`
	Failed   map[string]string `json:"failed,omitempty"` // ref -> error
}

// AcceptReviewItems records an approval for each ref through the single
// mutation path (SetItemTrust: countersignature + snapshots), continuing past
// per-item failures. It backs the porcelain's "accept all remaining in
// bundle" action.
func AcceptReviewItems(cfg *config.Config, req AcceptReviewItemsRequest) *AcceptReviewItemsResult {
	res := &AcceptReviewItemsResult{}
	for _, ref := range req.Refs {
		_, err := SetItemTrust(cfg, SetItemTrustRequest{
			Ref: ref, Project: req.Project, Signer: req.Signer,
			UserStore: req.UserStore, ProjectStore: req.ProjectStore,
			Loader: req.Loader, FS: req.FS,
		})
		if err != nil {
			if res.Failed == nil {
				res.Failed = make(map[string]string)
			}
			res.Failed[ref] = err.Error()
			continue
		}
		res.Accepted = append(res.Accepted, ref)
	}
	return res
}
