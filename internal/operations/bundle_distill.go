package operations

import (
	"context"
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/bundles"
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
		p := bundle.Prompts[name]
		if item, ok := planBundleItemDistill(ItemKindPrompt, name, p.NoDistill, p.NeedsDistill(), req.Force); ok {
			res.Items = append(res.Items, item)
		} else {
			promptTargets = append(promptTargets, name)
		}
	}

	if req.DryRun {
		appendStatus(&res.Items, ItemKindFragment, fragTargets, DistillStatusPlanned, "")
		appendStatus(&res.Items, ItemKindPrompt, promptTargets, DistillStatusPlanned, "")
		return res, nil
	}
	if req.Distiller == nil {
		appendStatus(&res.Items, ItemKindFragment, fragTargets, DistillStatusSkipped, "no_distiller")
		appendStatus(&res.Items, ItemKindPrompt, promptTargets, DistillStatusSkipped, "no_distiller")
		return res, nil
	}

	distillFragments(ctx, bundle, fragTargets, req.Distiller)
	distillPrompts(ctx, bundle, promptTargets, req.Distiller)
	for _, n := range fragTargets {
		res.Items = append(res.Items, DistillBundleItem{Kind: ItemKindFragment, Name: n, Status: DistillStatusDistilled, ModelID: bundle.Fragments[n].DistilledBy})
	}
	for _, n := range promptTargets {
		res.Items = append(res.Items, DistillBundleItem{Kind: ItemKindPrompt, Name: n, Status: DistillStatusDistilled, ModelID: bundle.Prompts[n].DistilledBy})
	}

	if len(fragTargets)+len(promptTargets) > 0 {
		store := req.Store
		if store == nil {
			store = bundles.NewFSStore(nil, false)
		}
		if err := store.Save(bundle); err != nil {
			return nil, fmt.Errorf("save %s: %w", req.Path, err)
		}
		res.Saved = true
	}
	return res, nil
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

func appendStatus(items *[]DistillBundleItem, kind ItemKind, names []string, status DistillBundleItemStatus, reason string) {
	for _, n := range names {
		*items = append(*items, DistillBundleItem{Kind: kind, Name: n, Status: status, Reason: reason})
	}
}
