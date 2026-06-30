package operations

import (
	"context"
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/shared/collections"
)

// Bundle items (fragments and prompts) are managed through this frontend-
// agnostic core: every write goes through the same symlink-traversal guard
// (loadBundleForUpdate), the same field-preserving merge (applyFragmentEdits /
// applyPromptEdits), and the same provider-agnostic distillation seam
// (Distiller). Frontends — the CLI today, an MCP tool tomorrow — supply
// arguments and render results; they do no bundle IO and host no LLM
// themselves. The terminal-only concerns (an $EDITOR round-trip, status lines)
// stay in the frontend; the library never touches stdin/stdout.

// ItemKind identifies which kind of bundle item an operation targets.
type ItemKind string

const (
	ItemKindFragment ItemKind = "fragment"
	ItemKindSkill    ItemKind = "skill"
)

func (k ItemKind) valid() bool { return k == ItemKindFragment || k == ItemKindSkill }

// ErrItemExists and ErrItemNotFound are sentinels so any frontend can map them
// to its own UX (a CLI message, an MCP error code) via errors.Is rather than
// string matching.
var (
	ErrItemExists   = errors.New("item already exists")
	ErrItemNotFound = errors.New("item not found")
)

// GetItemRequest identifies a single fragment/prompt to read.
type GetItemRequest struct {
	Bundle string   `json:"bundle"`
	Kind   ItemKind `json:"kind"`
	Name   string   `json:"name"`
}

// GetItemResult carries an item's raw and distilled content.
type GetItemResult struct {
	Content   string `json:"content"`
	Distilled string `json:"distilled,omitempty"`
}

// GetItemContent returns a bundle item's content (and distilled form). It is the
// read half the edit flow needs: a frontend fetches the current content, lets
// the user change it however it likes, then calls SetItemContent.
func GetItemContent(_ context.Context, cfg *config.Config, req GetItemRequest) (*GetItemResult, error) {
	if !req.Kind.valid() {
		return nil, fmt.Errorf("invalid item kind: %q", req.Kind)
	}
	bundle, err := bundleLoader(cfg).Load(req.Bundle)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Bundle, err)
	}
	content, distilled, ok := itemContent(bundle, req.Kind, req.Name)
	if !ok {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
	}
	return &GetItemResult{Content: content, Distilled: distilled}, nil
}

// AddItemRequest is the input for AddItem.
type AddItemRequest struct {
	Bundle  string   `json:"bundle"`
	Kind    ItemKind `json:"kind"`
	Name    string   `json:"name"`
	Content string   `json:"content"`

	// Distiller, when non-nil and the content is distillable, distills the new
	// item. Frontends inject an LLM-backed implementation (or nil to skip — the
	// CLI's placeholder-then-edit flow adds raw and distills on the later edit).
	Distiller Distiller `json:"-"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
}

// AddItemResult reports a created item.
type AddItemResult struct {
	Status string `json:"status"`
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// AddItem adds a fragment or prompt to an existing bundle. It is add-only:
// an existing name yields ErrItemExists rather than overwriting (the data-loss
// hazard that bars routing add through UpdateBundle's upsert SetFragments).
func AddItem(ctx context.Context, cfg *config.Config, req AddItemRequest) (*AddItemResult, error) {
	if !req.Kind.valid() {
		return nil, fmt.Errorf("invalid item kind: %q", req.Kind)
	}
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	if _, _, ok := itemContent(bundle, req.Kind, req.Name); ok {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemExists)
	}

	var distillTargets []string
	switch req.Kind {
	case ItemKindFragment:
		_, distillTargets = applyFragmentEdits(bundle,
			map[string]BundleFragmentInput{req.Name: {Content: req.Content}}, nil, nil)
		distillFragments(ctx, bundle, distillTargets, req.Distiller)
	case ItemKindSkill:
		_, distillTargets = applyPromptEdits(bundle,
			map[string]BundleSkillInput{req.Name: {Content: req.Content}}, nil, nil)
		distillPrompts(ctx, bundle, distillTargets, req.Distiller)
	}

	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}
	return &AddItemResult{Status: "created", Bundle: req.Bundle, Name: req.Name, Path: bundle.Path}, nil
}

// DeleteItemRequest is the input for DeleteItem.
type DeleteItemRequest struct {
	Bundle string   `json:"bundle"`
	Kind   ItemKind `json:"kind"`
	Name   string   `json:"name"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
}

// DeleteItemResult reports a removed item.
type DeleteItemResult struct {
	Status string `json:"status"`
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// DeleteItem removes a fragment or prompt from a bundle, returning
// ErrItemNotFound when the named item is absent.
func DeleteItem(_ context.Context, cfg *config.Config, req DeleteItemRequest) (*DeleteItemResult, error) {
	if !req.Kind.valid() {
		return nil, fmt.Errorf("invalid item kind: %q", req.Kind)
	}
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	if _, _, ok := itemContent(bundle, req.Kind, req.Name); !ok {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
	}
	switch req.Kind {
	case ItemKindFragment:
		delete(bundle.Fragments, req.Name)
	case ItemKindSkill:
		delete(bundle.Skills, req.Name)
	}
	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}
	return &DeleteItemResult{Status: "deleted", Bundle: req.Bundle, Name: req.Name, Path: bundle.Path}, nil
}

// SetItemContentRequest is the input for SetItemContent.
type SetItemContentRequest struct {
	Bundle  string   `json:"bundle"`
	Kind    ItemKind `json:"kind"`
	Name    string   `json:"name"`
	Content string   `json:"content"`

	// Distiller, when non-nil, (re)distills the item after the content change
	// unless the item is marked no_distill. Injected by the frontend.
	Distiller Distiller `json:"-"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
}

// SetItemContentResult reports an updated item.
type SetItemContentResult struct {
	Status    string `json:"status"`
	Bundle    string `json:"bundle"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Distilled bool   `json:"distilled"`
}

// SetItemContent replaces an existing item's content (the write half of an edit
// flow), preserving its other fields (tags, notes, installation, no_distill).
// A content change clears the stale distilled form and, when a Distiller is
// supplied and the item is distillable, regenerates it.
func SetItemContent(ctx context.Context, cfg *config.Config, req SetItemContentRequest) (*SetItemContentResult, error) {
	if !req.Kind.valid() {
		return nil, fmt.Errorf("invalid item kind: %q", req.Kind)
	}
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}

	var distillTargets []string
	var failed collections.Set[string]
	switch req.Kind {
	case ItemKindFragment:
		existing, ok := bundle.Fragments[req.Name]
		if !ok {
			return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
		}
		// Carry the existing fields through so the content-only edit can't wipe
		// them (applyFragmentEdits replaces tags/notes/installation from input).
		_, distillTargets = applyFragmentEdits(bundle, map[string]BundleFragmentInput{req.Name: {
			Content:      req.Content,
			Tags:         existing.Tags,
			Notes:        existing.Notes,
			Installation: existing.Installation,
			NoDistill:    existing.NoDistill,
		}}, nil, nil)
		failed = distillFragments(ctx, bundle, distillTargets, req.Distiller)
	case ItemKindSkill:
		existing, ok := bundle.Skills[req.Name]
		if !ok {
			return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
		}
		_, distillTargets = applyPromptEdits(bundle, map[string]BundleSkillInput{req.Name: {
			Content:      req.Content,
			Description:  existing.Description,
			Tags:         existing.Tags,
			Notes:        existing.Notes,
			Installation: existing.Installation,
			NoDistill:    existing.NoDistill,
		}}, nil, nil)
		failed = distillPrompts(ctx, bundle, distillTargets, req.Distiller)
	}

	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}
	return &SetItemContentResult{
		Status: "updated",
		Bundle: req.Bundle,
		Name:   req.Name,
		Path:   bundle.Path,
		// A warn-and-skip distill failure leaves the item with no distilled form
		// (applyFragmentEdits already cleared the stale one), so reporting
		// Distilled=true would be a lie — only claim it when the distiller ran
		// without erroring on this item.
		Distilled: req.Distiller != nil && len(distillTargets) > 0 && !failed.Has(req.Name),
	}, nil
}

// DistillItemRequest is the input for DistillItem.
type DistillItemRequest struct {
	Bundle string   `json:"bundle"`
	Kind   ItemKind `json:"kind"`
	Name   string   `json:"name"`
	Force  bool     `json:"force"` // distill even when the content is unchanged

	// Distiller performs the distillation. A nil Distiller is reported as a
	// skip (no engine available) rather than a silent no-op.
	Distiller Distiller `json:"-"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
}

// DistillItemResult reports the outcome. Status is "distilled" or "skipped";
// when skipped, Reason is one of "no_distill", "unchanged", "no_distiller",
// "distill_failed".
type DistillItemResult struct {
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Bundle  string `json:"bundle"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	ModelID string `json:"model_id,omitempty"`
}

// DistillItem (re)distills a single fragment or prompt in place — the force-
// redistill operation behind `ctxloom <kind> distill`. It honors the item's
// no_distill flag and the content-hash freshness check (unless Force), and runs
// the actual distillation through the injected Distiller so the LLM stays out of
// the core.
func DistillItem(ctx context.Context, cfg *config.Config, req DistillItemRequest) (*DistillItemResult, error) {
	if !req.Kind.valid() {
		return nil, fmt.Errorf("invalid item kind: %q", req.Kind)
	}
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	noDistill, needsDistill, ok := itemDistillState(bundle, req.Kind, req.Name)
	if !ok {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
	}

	skip := func(reason string) *DistillItemResult {
		return &DistillItemResult{Status: "skipped", Reason: reason, Bundle: req.Bundle, Name: req.Name, Path: bundle.Path}
	}
	switch {
	case noDistill:
		return skip("no_distill"), nil
	case !req.Force && !needsDistill:
		return skip("unchanged"), nil
	case req.Distiller == nil:
		return skip("no_distiller"), nil
	}

	var failed collections.Set[string]
	switch req.Kind {
	case ItemKindFragment:
		failed = distillFragments(ctx, bundle, []string{req.Name}, req.Distiller)
	case ItemKindSkill:
		failed = distillPrompts(ctx, bundle, []string{req.Name}, req.Distiller)
	}
	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}
	_, modelID := itemDistilled(bundle, req.Kind, req.Name)
	// distillFragments/distillPrompts warn-and-skip on a Distiller error: a
	// failed re-distill leaves the stale previous DistilledBy intact and a
	// first-time failure leaves it empty. Reporting either as a fresh
	// "distilled" success is a silent lie (mirrors distillOutcome on the bundle
	// path), so surface the failure as a skip instead.
	if failed.Has(req.Name) || modelID == "" {
		return skip("distill_failed"), nil
	}
	return &DistillItemResult{
		Status:  "distilled",
		Bundle:  req.Bundle,
		Name:    req.Name,
		Path:    bundle.Path,
		ModelID: modelID,
	}, nil
}

// MCP servers are a third kind of bundle item, but unlike fragments/prompts
// they carry no distilled content, so they get a focused get/set pair rather
// than joining the ItemKind machinery.

// GetBundleMCPRequest identifies an MCP server to read.
type GetBundleMCPRequest struct {
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
}

// GetBundleMCPResult carries an MCP server's configuration.
type GetBundleMCPResult struct {
	MCP bundles.BundleMCP `json:"mcp"`
}

// GetBundleMCP returns a bundle's MCP server config, or ErrItemNotFound. The
// read half of an MCP edit; the frontend renders it however it likes.
func GetBundleMCP(_ context.Context, cfg *config.Config, req GetBundleMCPRequest) (*GetBundleMCPResult, error) {
	bundle, err := bundleLoader(cfg).Load(req.Bundle)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Bundle, err)
	}
	mcp, ok := bundle.MCP[req.Name]
	if !ok {
		return nil, fmt.Errorf("mcp %q: %w", req.Name, ErrItemNotFound)
	}
	return &GetBundleMCPResult{MCP: mcp}, nil
}

// SetBundleMCPRequest is the input for SetBundleMCP.
type SetBundleMCPRequest struct {
	Bundle string         `json:"bundle"`
	Name   string         `json:"name"`
	MCP    BundleMCPInput `json:"mcp"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
}

// SetBundleMCPResult reports an updated MCP server.
type SetBundleMCPResult struct {
	Status string `json:"status"`
	Bundle string `json:"bundle"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// SetBundleMCP upserts an MCP server config into a bundle (symlink-guarded
// save), the write half of an MCP edit. The frontend supplies a structured
// BundleMCPInput — it never hands the core its editor's raw YAML.
func SetBundleMCP(_ context.Context, cfg *config.Config, req SetBundleMCPRequest) (*SetBundleMCPResult, error) {
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Bundle)
	if err != nil {
		return nil, err
	}
	applyMCPEdits(bundle, map[string]BundleMCPInput{req.Name: req.MCP}, nil, nil)
	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}
	return &SetBundleMCPResult{Status: "updated", Bundle: req.Bundle, Name: req.Name, Path: bundle.Path}, nil
}

// itemContent returns the raw and distilled content of a named item, and whether
// it exists, for either kind.
func itemContent(b *bundles.Bundle, kind ItemKind, name string) (content, distilled string, ok bool) {
	switch kind {
	case ItemKindFragment:
		if f, exists := b.Fragments[name]; exists {
			return f.Content, f.Distilled, true
		}
	case ItemKindSkill:
		if p, exists := b.Skills[name]; exists {
			return p.Content, p.Distilled, true
		}
	}
	return "", "", false
}

// itemDistillState reports an item's no_distill flag and whether its distilled
// form is stale (content changed since last distill), plus whether it exists.
func itemDistillState(b *bundles.Bundle, kind ItemKind, name string) (noDistill, needsDistill, ok bool) {
	switch kind {
	case ItemKindFragment:
		if f, exists := b.Fragments[name]; exists {
			return f.NoDistill, f.NeedsDistill(), true
		}
	case ItemKindSkill:
		if p, exists := b.Skills[name]; exists {
			return p.NoDistill, p.NeedsDistill(), true
		}
	}
	return false, false, false
}

// itemDistilled returns an item's distilled content and the model that produced
// it.
func itemDistilled(b *bundles.Bundle, kind ItemKind, name string) (distilled, modelID string) {
	switch kind {
	case ItemKindFragment:
		f := b.Fragments[name]
		return f.Distilled, f.DistilledBy
	case ItemKindSkill:
		p := b.Skills[name]
		return p.Distilled, p.DistilledBy
	}
	return "", ""
}
