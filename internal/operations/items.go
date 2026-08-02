package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
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
	ItemKindCommand  ItemKind = "command"
)

func (k ItemKind) valid() bool { return k == ItemKindFragment || k == ItemKindCommand }

// ErrItemExists and ErrItemNotFound are sentinels so any frontend can map them
// to its own UX (a CLI message, an MCP error code) via errors.Is rather than
// string matching.
var (
	ErrItemExists   = errors.New("item already exists")
	ErrItemNotFound = errors.New("item not found")
	// ErrItemContentEmpty guards SetItemContent's floor: this is the
	// frontend-agnostic operations layer any caller (CLI, MCP, a future
	// frontend) reaches directly, so the empty-buffer floor belongs here, not
	// only in the CLI's own editItem/checkEditedContent pre-check.
	ErrItemContentEmpty = errors.New("refusing to overwrite existing content with an empty buffer")
)

// GetItemRequest identifies a single fragment/prompt to read.
type GetItemRequest struct {
	Bundle string   `json:"bundle"`
	Kind   ItemKind `json:"kind"`
	Name   string   `json:"name"`
}

// GetItemResult carries an item's raw and distilled content, plus its
// persistent no_distill flag — a frontend deciding whether a per-invocation
// distill-skip (e.g. `edit --no-distill`) is redundant with the item's own
// standing setting needs this without a second bundle load.
type GetItemResult struct {
	Content   string `json:"content"`
	Distilled string `json:"distilled,omitempty"`
	NoDistill bool   `json:"no_distill,omitempty"`
}

// GetItemContent returns a bundle item's content (and distilled form). It is the
// read half the edit flow needs: a frontend fetches the current content, lets
// the user change it however it likes, then calls SetItemContent. It is also the
// whole "show one item" read path — a frontend needs no bundle IO of its own.
//
// Bundle resolution goes through GetBundle, the one read-path bundle resolver,
// so a short "<remote>/<bundle>" argument is canonicalized here exactly as it is
// for `bundle show`. Loading through the raw seeded loader instead skipped that
// canonicalization, and the same ref one command accepted the next reported
// missing.
//
// A missing item's error carries the names that DO exist: the caller asked for
// something by name, and the answer to a typo is the list, not a bare "not
// found". ErrItemNotFound still wraps, so errors.Is callers are unaffected.
func GetItemContent(_ context.Context, cfg *config.Config, req GetItemRequest) (*GetItemResult, error) {
	if !req.Kind.valid() {
		return nil, fmt.Errorf("invalid item kind: %q", req.Kind)
	}
	bundle, err := GetBundle(cfg, req.Bundle)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Bundle, err)
	}
	item, ok := lookupItem(bundle, req.Kind, req.Name)
	if !ok {
		return nil, fmt.Errorf("%s %q: %w\n\nAvailable %ss: %s",
			req.Kind, req.Name, ErrItemNotFound, req.Kind, strings.Join(itemNames(bundle, req.Kind), ", "))
	}
	return &GetItemResult{Content: item.content, Distilled: item.distilled, NoDistill: item.noDistill}, nil
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
	if _, ok := lookupItem(bundle, req.Kind, req.Name); ok {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemExists)
	}

	var distillTargets []string
	switch req.Kind {
	case ItemKindFragment:
		_, distillTargets = applyFragmentEdits(bundle,
			map[string]BundleFragmentInput{req.Name: {Content: req.Content}}, nil, nil)
		distillFragments(ctx, bundle, distillTargets, req.Distiller)
	case ItemKindCommand:
		_, distillTargets = applyPromptEdits(bundle,
			map[string]BundleCommandInput{req.Name: {Content: req.Content}}, nil, nil)
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
	if _, ok := lookupItem(bundle, req.Kind, req.Name); !ok {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
	}
	switch req.Kind {
	case ItemKindFragment:
		delete(bundle.Fragments, req.Name)
	case ItemKindCommand:
		delete(bundle.Commands, req.Name)
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
	// This used to accept Content: "" unconditionally, silently
	// overwriting an authored fragment/command with zero bytes and reporting
	// status: "updated" — the CLI's own editItem path is guarded by
	// checkEditedContent, but that guard sits above this frontend-agnostic
	// function (ADR-0026: operations is what CLI AND MCP call into), so any
	// other caller reached the same silent no-op. strings.TrimSpace matches
	// checkEditedContent's own criterion (whitespace-only is still empty).
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemContentEmpty)
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
	case ItemKindCommand:
		existing, ok := bundle.Commands[req.Name]
		if !ok {
			return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
		}
		_, distillTargets = applyPromptEdits(bundle, map[string]BundleCommandInput{req.Name: {
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
	item, ok := lookupItem(bundle, req.Kind, req.Name)
	if !ok {
		return nil, fmt.Errorf("%s %q: %w", req.Kind, req.Name, ErrItemNotFound)
	}

	skip := func(reason string) *DistillItemResult {
		return &DistillItemResult{Status: "skipped", Reason: reason, Bundle: req.Bundle, Name: req.Name, Path: bundle.Path}
	}
	switch {
	case item.noDistill:
		return skip("no_distill"), nil
	case !req.Force && !item.needsDistill:
		return skip("unchanged"), nil
	case req.Distiller == nil:
		return skip("no_distiller"), nil
	}

	var failed collections.Set[string]
	switch req.Kind {
	case ItemKindFragment:
		failed = distillFragments(ctx, bundle, []string{req.Name}, req.Distiller)
	case ItemKindCommand:
		failed = distillPrompts(ctx, bundle, []string{req.Name}, req.Distiller)
	}
	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}
	distilled, _ := lookupItem(bundle, req.Kind, req.Name)
	modelID := distilled.distilledBy
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

// itemFields is the read-side projection of a bundle item: every field the item
// operations read, regardless of the kind they were asked about. Fragments and
// commands are separate types with no common interface, so one struct is what
// lets the read side be kind-agnostic.
type itemFields struct {
	content      string
	distilled    string
	distilledBy  string
	noDistill    bool
	needsDistill bool
}

// itemNames lists the names of every item of kind in b, for the "you asked for a
// name that isn't here, here are the ones that are" half of a not-found error.
func itemNames(b *bundles.Bundle, kind ItemKind) []string {
	switch kind {
	case ItemKindFragment:
		return b.FragmentNames()
	case ItemKindCommand:
		return b.PromptNames()
	}
	return nil
}

// lookupItem projects a named item of either kind, reporting whether it exists.
// It is the ONE place the read side switches on ItemKind: three separate
// switches over the same two-kind vocabulary meant a new kind compiled cleanly
// while silently answering "absent" on whichever of them was forgotten, and a
// caller that wanted two field groups paid for two lookups of the same item.
func lookupItem(b *bundles.Bundle, kind ItemKind, name string) (itemFields, bool) {
	switch kind {
	case ItemKindFragment:
		if f, exists := b.Fragments[name]; exists {
			return itemFields{
				content:      f.Content,
				distilled:    f.Distilled,
				distilledBy:  f.DistilledBy,
				noDistill:    f.NoDistill,
				needsDistill: f.NeedsDistill(),
			}, true
		}
	case ItemKindCommand:
		if p, exists := b.Commands[name]; exists {
			return itemFields{
				content:      p.Content,
				distilled:    p.Distilled,
				distilledBy:  p.DistilledBy,
				noDistill:    p.NoDistill,
				needsDistill: p.NeedsDistill(),
			}, true
		}
	}
	return itemFields{}, false
}
