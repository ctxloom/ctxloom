package operations

import (
	"context"
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// DistillBundleFileRequest is the input for DistillBundleFile.
type DistillBundleFileRequest struct {
	Path   string `json:"path"`
	Force  bool   `json:"force"`   // distill even unchanged items
	DryRun bool   `json:"dry_run"` // report what would distill; save nothing

	Distiller Distiller `json:"-"`

	// Store, when non-nil, is the bundle storage adapter the result is saved
	// through (ADR 0026); nil defaults to the filesystem.
	Store bundles.Store `json:"-"`

	// Cfg locates the countersignature stores consulted for the re-distill
	// invalidation report (spec §10.4, DistillBundleFileResult.Invalidated).
	// Optional: nil still checks the default (real OS / HOME) stores.
	Cfg *config.Config `json:"-"`
}

// DistillBundleItemStatus is an item's per-file distill outcome.
type DistillBundleItemStatus string

const (
	DistillStatusDistilled DistillBundleItemStatus = "distilled"
	DistillStatusSkipped   DistillBundleItemStatus = "skipped"
	DistillStatusPlanned   DistillBundleItemStatus = "planned" // dry-run
)

// DistillBundleItem reports one item's outcome.
type DistillBundleItem struct {
	Kind    ItemKind                `json:"kind"`
	Name    string                  `json:"name"`
	Status  DistillBundleItemStatus `json:"status"`
	Reason  string                  `json:"reason,omitempty"` // skipped: no_distill | unchanged | no_distiller
	ModelID string                  `json:"model_id,omitempty"`
}

// DistillBundleFileResult reports the per-item outcomes and whether the file was
// written.
type DistillBundleFileResult struct {
	Path  string              `json:"path"`
	Items []DistillBundleItem `json:"items"`
	Saved bool                `json:"saved"`

	// Invalidated lists the "<kind>/<name>" of every item in this file whose
	// DISTILLED bytes just changed AND had a prior approve countersignature
	// recorded over the distilled form — that approval no longer verifies
	// (the bytes it covered no longer exist), so the item is back to pending
	// (spec §10.4). This is the re-distill LOUD PATH: report it at the moment
	// it is caused, not silently at the next `ctxloom review`.
	Invalidated []string `json:"invalidated,omitempty"`
}

// DistillBundleFile distills every distillable item in a bundle file and saves
// it — the file-oriented author tool behind `bundle distill` (it operates on a
// path, not a name in config). It skips no_distill items and, unless Force,
// unchanged ones; with DryRun it reports what would be distilled and writes
// nothing. Distillation runs through the injected Distiller; a nil Distiller
// turns every distillable item into a "no_distiller" skip rather than a silent
// no-op. Frontends own glob expansion, progress rendering, and the run summary.
func DistillBundleFile(ctx context.Context, req DistillBundleFileRequest) (*DistillBundleFileResult, error) {
	data, err := os.ReadFile(req.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", req.Path, err)
	}
	bundle, err := bundles.ParseBundle(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", req.Path, err)
	}
	bundle.Path = req.Path
	bundle.Name = bundles.ExtractBundleName(req.Path)

	res := &DistillBundleFileResult{Path: req.Path}
	var fragTargets, promptTargets []string

	for _, name := range bundle.FragmentNames() {
		f := bundle.Fragments[name]
		if item, ok := planBundleItemDistill(ItemKindFragment, name, f.NoDistill, f.NeedsDistill(), req.Force); ok {
			res.Items = append(res.Items, item)
		} else {
			fragTargets = append(fragTargets, name)
		}
	}
	for _, name := range bundle.PromptNames() {
		p := bundle.Commands[name]
		if item, ok := planBundleItemDistill(ItemKindCommand, name, p.NoDistill, p.NeedsDistill(), req.Force); ok {
			res.Items = append(res.Items, item)
		} else {
			promptTargets = append(promptTargets, name)
		}
	}

	if req.DryRun {
		appendStatus(&res.Items, ItemKindFragment, fragTargets, DistillStatusPlanned, "")
		appendStatus(&res.Items, ItemKindCommand, promptTargets, DistillStatusPlanned, "")
		return res, nil
	}
	if req.Distiller == nil {
		appendStatus(&res.Items, ItemKindFragment, fragTargets, DistillStatusSkipped, "no_distiller")
		appendStatus(&res.Items, ItemKindCommand, promptTargets, DistillStatusSkipped, "no_distiller")
		return res, nil
	}

	failedFrags := distillFragments(ctx, bundle, fragTargets, req.Distiller)
	failedPrompts := distillPrompts(ctx, bundle, promptTargets, req.Distiller)
	for _, n := range fragTargets {
		res.Items = append(res.Items, distillOutcome(ItemKindFragment, n, bundle.Fragments[n].Distilled, bundle.Fragments[n].DistilledBy, failedFrags.Has(n)))
	}
	for _, n := range promptTargets {
		res.Items = append(res.Items, distillOutcome(ItemKindCommand, n, bundle.Commands[n].Distilled, bundle.Commands[n].DistilledBy, failedPrompts.Has(n)))
	}

	if anyDistilled(res.Items) {
		store := req.Store
		if store == nil {
			store = bundles.NewFSStore(nil, nil)
		}
		if err := store.Save(bundle); err != nil {
			return nil, fmt.Errorf("save %s: %w", req.Path, err)
		}
		res.Saved = true
		res.Invalidated = invalidatedByDistill(req.Cfg, bundle.Name, res.Items)
	}
	return res, nil
}

// anyDistilled reports whether this run produced new distilled bytes for at
// least one item — the only condition under which the file has anything new to
// hold, and therefore the only condition under which it may be rewritten.
//
// Having TARGETS is not that condition. distillFragments/distillPrompts leave
// the bundle completely untouched on a per-item failure (they warn and skip, so
// a failed re-distill keeps its previous distillation), so a run where every
// attempt failed has nothing to persist. Saving anyway is not a harmless
// no-op: Store.Save re-marshals the document, which loses the author's comments
// and key order, and the re-marshalled bytes no longer match any detached
// publisher signature — so invalidateStaleSignature then DELETES the author's
// signature. An offline LLM would have cost them their comments and their
// signature, in exchange for nothing.
func anyDistilled(items []DistillBundleItem) bool {
	for _, it := range items {
		if it.Status == DistillStatusDistilled {
			return true
		}
	}
	return false
}

// invalidatedByDistill checks each successfully-DISTILLED item against the
// countersignature stores' sidecar index (display-only) for a prior approve
// record over the DISTILLED form: presence proves invalidation (see
// countersignRecords.hadPriorApprove). bundleName addresses this as a LOCAL
// bundle (ctxloom:local) — the addressing `bundle distill <path>` operates
// under; best-effort and never errors.
func invalidatedByDistill(cfg *config.Config, bundleName string, items []DistillBundleItem) []string {
	records := buildCountersignRecords(cfg, nil, nil, nil, nil)
	var out []string
	for _, it := range items {
		if it.Status != DistillStatusDistilled {
			continue
		}
		tKind, ok := distillItemKindToTrust(it.Kind)
		if !ok {
			continue
		}
		ref := trust.Ref{Bundle: bundleName, Kind: tKind, Name: it.Name, IsLocal: true}
		prior, err := records.hadPriorApprove(CountersignRef(ref), signing.FormDistilled)
		if err != nil {
			// Cannot tell whether this item had a prior approval — say so and
			// list it anyway. Over-warning costs a re-review; under-warning
			// leaves a signature the user believes still covers these bytes.
			clidiag.Warn("ctxloom", "distill: cannot read the approvals index for %s/%s, assuming its approval is invalidated: %v", it.Kind, it.Name, err)
			prior = true
		}
		if prior {
			out = append(out, string(it.Kind)+"/"+it.Name)
		}
	}
	return out
}

// distillItemKindToTrust maps operations.ItemKind onto trust.ItemKind (the
// two kinds distill touches: fragment, command/prompt).
func distillItemKindToTrust(k ItemKind) (trust.ItemKind, bool) {
	switch k {
	case ItemKindFragment:
		return trust.KindFragment, true
	case ItemKindCommand:
		return trust.KindPrompt, true
	default:
		return "", false
	}
}

// planBundleItemDistill returns a skip item (and ok=true) when an item should be
// skipped, or ok=false when it's a distill target.
func planBundleItemDistill(kind ItemKind, name string, noDistill, needsDistill, force bool) (DistillBundleItem, bool) {
	if noDistill {
		return DistillBundleItem{Kind: kind, Name: name, Status: DistillStatusSkipped, Reason: "no_distill"}, true
	}
	if !force && !needsDistill {
		return DistillBundleItem{Kind: kind, Name: name, Status: DistillStatusSkipped, Reason: "unchanged"}, true
	}
	return DistillBundleItem{}, false
}

// distillOutcome classifies one already-attempted item. distillFragments/
// distillPrompts warn-and-skip on a per-item error and report it via their
// failed set: post-state alone is not enough, because a failed RE-distill
// leaves the previous Distilled/DistilledBy intact — reporting that stale
// model id as a fresh success was a silent lie in the summary. A first-time
// failure also leaves DistilledBy empty, which the empty-check still catches
// for callers without a failed set.
//
// It is handed the distilled CONTENT as well as the model id so that "a model
// id was stamped" can never on its own buy a success verdict: zero delivered
// bytes is a failure however it arose. A classifier that cannot see what was
// produced is a classifier that can be lied to.
func distillOutcome(kind ItemKind, name, distilled, distilledBy string, failed bool) DistillBundleItem {
	if failed || distilledBy == "" || distilled == "" {
		return DistillBundleItem{Kind: kind, Name: name, Status: DistillStatusSkipped, Reason: "distill_failed"}
	}
	return DistillBundleItem{Kind: kind, Name: name, Status: DistillStatusDistilled, ModelID: distilledBy}
}

func appendStatus(items *[]DistillBundleItem, kind ItemKind, names []string, status DistillBundleItemStatus, reason string) {
	for _, n := range names {
		*items = append(*items, DistillBundleItem{Kind: kind, Name: n, Status: status, Reason: reason})
	}
}
