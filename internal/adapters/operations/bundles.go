// Package operations: bundle authoring.
//
// CreateBundle/UpdateBundle/DeleteBundle/PushBundle expose the same authoring
// surface as `cmd/bundle.go` but in a Go-callable form usable from the MCP
// server. The CLI predates this layer; refactoring it to delegate here is a
// follow-up.
package operations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// DistillKind tags whether a Distill call is for a fragment or a prompt.
// The real LLM-backed distiller uses this to scope the sibling-context it
// builds (excluding the item being distilled).
type DistillKind string

const (
	DistillKindFragment DistillKind = "fragment"
	DistillKindCommand  DistillKind = "command"
)

// DistillRequest carries everything an implementation needs to compress a
// single item. Bundle is the in-flight bundle (post-merge, pre-save) so the
// distiller can read sibling items for context-aware compression.
type DistillRequest struct {
	Kind    DistillKind
	Name    string
	Content string
	Bundle  *bundles.Bundle
}

// DistillResult is what an implementation returns on success.
type DistillResult struct {
	Distilled string
	ModelID   string
}

// Distiller compresses fragment/prompt content via an LLM. The operations
// layer is provider-agnostic: callers (MCP server, future CLI refactor) wire
// up a real LLM-backed implementation; tests inject a mock. A nil Distiller
// means "skip distillation"; bundles still save with raw content.
type Distiller interface {
	Distill(ctx context.Context, req DistillRequest) (DistillResult, error)
}

// CreateBundleRequest is the input for CreateBundle.
type CreateBundleRequest struct {
	Name        string                         `json:"name"`
	Description string                         `json:"description,omitempty"`
	Version     string                         `json:"version,omitempty"` // Defaults to "1.0.0".
	Tags        []string                       `json:"tags,omitempty"`
	Author      string                         `json:"author,omitempty"`
	Fragments   map[string]BundleFragmentInput `json:"fragments,omitempty"`
	Commands    map[string]BundleCommandInput  `json:"commands,omitempty"`
	MCPServers  map[string]BundleMCPInput      `json:"mcp_servers,omitempty"`

	// Distiller, when non-nil, is invoked for each new fragment whose
	// NoDistill flag is unset. Distill failures are warned to stderr but
	// do not fail the create (fault-tolerance principle).
	Distiller Distiller `json:"-"`

	// Store, when non-nil, is the bundle storage adapter to persist through
	// (ADR 0026); nil defaults to the filesystem. Frontends leave it nil.
	Store bundles.Store `json:"-"`

	// FS, when non-nil, is the afero filesystem the tree is written to (and
	// checked for an existing bundle on) when no Store is given; nil defaults
	// to the session's store and the OS filesystem.
	FS afero.Fs `json:"-"`
}

// BundleFragmentInput describes a fragment to add or update via operations.
// Distillation is governed by NoDistill plus the request-level distill toggle
// in UpdateBundleRequest (see L3/L4 tests). BundleFragment has no Description
// field; use Notes for human-readable annotations not sent to the AI and
// Installation for setup instructions surfaced on install.
//
// A set REPLACES every field, Premise included: a caller editing an existing
// fragment must carry its current Premise (see SetItemContent), or the edit
// makes a conditional fragment unconditional — "" means always load.
type BundleFragmentInput struct {
	Content      string   `json:"content"`
	Premise      string   `json:"premise,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	Installation string   `json:"installation,omitempty"`
	NoDistill    bool     `json:"no_distill,omitempty"`
}

// BundleCommandInput describes a command to add or update via operations.
type BundleCommandInput struct {
	Content      string   `json:"content"`
	Description  string   `json:"description,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	Installation string   `json:"installation,omitempty"`
	NoDistill    bool     `json:"no_distill,omitempty"`
}

// BundleMCPInput describes an MCP server entry to add or update via operations.
// BundleMCP has no Description; use Notes for AI-invisible annotations and
// Installation for setup instructions surfaced to the AI on install.
type BundleMCPInput struct {
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Notes        string            `json:"notes,omitempty"`
	Installation string            `json:"installation,omitempty"`
}

// CreateBundleResult is what CreateBundle returns on success.
type CreateBundleResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// CreateBundle scaffolds a new bundle TREE at
// .ctxloom/content/bundles/v2/<name>/ — bundle.yaml plus one file per item —
// in the COMMITTED content tree, always. Creation is repo-local by definition:
// a new bundle is this project's own authored content, git-tracked from the
// moment it exists, and it takes no remote/destination (choosing where a
// bundle goes happens later, at push time). Writing it to the gitignored cache
// instead is how authored work ends up untracked and unsignable.
//
// A bundle created with no items is its envelope alone: the author's scaffold,
// which the local reader reads as an empty bundle until items are added.
//
// Path safety: req.Name is validated via bundles.ValidateBundleName before
// any filesystem call, so names containing "..", absolute paths, or null
// bytes are rejected. Slash-separated names like "personal/foo" are
// supported and land at .ctxloom/content/bundles/v2/personal/foo/. In
// addition, requireSafeBundlePath rejects any directory component already on
// disk that is a symlink — without this an attacker who had planted
// .ctxloom/content/bundles/personal -> /etc could induce Save to write outside
// the bundles root.
func CreateBundle(ctx context.Context, cfg *config.Config, req CreateBundleRequest) (*CreateBundleResult, error) {
	if err := validateCreateBundle(cfg, req); err != nil {
		return nil, err
	}

	dir := paths.LocalBundlesPathFor(cfg.GetAppPaths()[0], paths.LayoutV2)
	path := filepath.Join(dir, req.Name, bundles.DirectoryFormManifest)
	if err := requireSafeBundlePath([]string{dir}, path); err != nil {
		return nil, err
	}
	// Fail fast on a bundle that already exists, before distillation spends an
	// LLM round trip per item on a create that cannot land. This check is an
	// economy, NOT the decision: it is racy by construction, and the window it
	// opens is as long as distillation takes. claimNewBundleDir decides.
	fsys := getFS(req.FS)
	if err := refuseExistingBundle(fsys, path); err != nil {
		return nil, err
	}

	bundle := newCreatedBundle(req, path)
	applyFragmentInputs(bundle, req.Fragments)
	applyPromptInputs(bundle, req.Commands)
	applyMCPInputs(bundle, req.MCPServers)

	distillFragments(ctx, bundle, namesNeedingFragmentDistill(bundle, req.Fragments), req.Distiller)
	distillPrompts(ctx, bundle, namesNeedingPromptDistill(bundle, req.Commands), req.Distiller)

	if err := claimNewBundleDir(fsys, filepath.Dir(path)); err != nil {
		return nil, err
	}
	store := req.Store
	if store == nil && req.FS != nil {
		store = bundles.NewFSStore(req.FS, nil)
	}
	if err := bundleStore(cfg, store).Save(bundle); err != nil {
		// The claim is an empty directory nobody can use. Drop it so it
		// neither reads as a bundle nor blocks the retry.
		_ = fsys.RemoveAll(filepath.Dir(path))
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}

	return &CreateBundleResult{
		Status: "created",
		Name:   req.Name,
		Path:   path,
	}, nil
}

// validateCreateBundle refuses a missing or unsafe name, and a config with
// no .ctxloom directory to create into.
func validateCreateBundle(cfg *config.Config, req CreateBundleRequest) error {
	if req.Name == "" {
		return fmt.Errorf("name is required")
	}
	if err := bundles.ValidateBundleName(req.Name); err != nil {
		return err
	}
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return fmt.Errorf("no .ctxloom directory configured")
	}
	return nil
}

// refuseExistingBundle fails when a bundle manifest is already at path.
func refuseExistingBundle(fsys afero.Fs, path string) error {
	exists, err := afero.Exists(fsys, path)
	if err != nil {
		return fmt.Errorf("failed to check for an existing bundle: %w", err)
	}
	if exists {
		return fmt.Errorf("bundle already exists: %s", path)
	}
	return nil
}

// newCreatedBundle is the new bundle's metadata, versioned 1.0.0 unless the
// request names a version.
func newCreatedBundle(req CreateBundleRequest, path string) *bundles.Bundle {
	version := req.Version
	if version == "" {
		version = "1.0.0"
	}
	return &bundles.Bundle{
		Version:     version,
		Description: req.Description,
		Tags:        req.Tags,
		Author:      req.Author,
		Path:        path,
	}
}

// claimNewBundleDir claims dir — a brand-new bundle's own tree directory —
// atomically. Creation is "write only if absent", and a check-then-save cannot
// express that: anything appearing at the path in between — a concurrent
// `bundle create`, a pull, another author on a shared checkout — would be
// merged into, and what is overwritten is authored content nobody has a copy
// of. A single Mkdir makes the existence test and the claim one operation, so
// the loser of a race gets the same "already exists" refusal as a serial
// caller.
func claimNewBundleDir(fsys afero.Fs, dir string) error {
	if err := fsys.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("failed to create bundles directory: %w", err)
	}
	if err := fsys.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("bundle already exists: %s", dir)
		}
		return fmt.Errorf("failed to create bundle directory: %w", err)
	}
	return nil
}

// UpdateBundleRequest is the input for UpdateBundle. Pointer fields
// distinguish "set this field" from "leave alone"; slice/map fields default to
// no-op when empty.
type UpdateBundleRequest struct {
	Name string `json:"name"`

	SetDescription *string  `json:"set_description,omitempty"`
	SetVersion     *string  `json:"set_version,omitempty"`
	AddTags        []string `json:"add_tags,omitempty"`
	RemoveTags     []string `json:"remove_tags,omitempty"`

	// Add* are create-if-absent: an entry whose name already exists is left
	// untouched (no clobber). Set* are upsert. The CLI `bundle edit --add-*`
	// flags map to Add*; MCP tools that overwrite use Set*.
	AddFragments    map[string]BundleFragmentInput `json:"add_fragments,omitempty"`
	SetFragments    map[string]BundleFragmentInput `json:"set_fragments,omitempty"`
	RemoveFragments []string                       `json:"remove_fragments,omitempty"`

	AddPrompts    map[string]BundleCommandInput `json:"add_prompts,omitempty"`
	SetPrompts    map[string]BundleCommandInput `json:"set_prompts,omitempty"`
	RemovePrompts []string                      `json:"remove_prompts,omitempty"`

	AddMCPServers    map[string]BundleMCPInput `json:"add_mcp_servers,omitempty"`
	SetMCPServers    map[string]BundleMCPInput `json:"set_mcp_servers,omitempty"`
	RemoveMCPServers []string                  `json:"remove_mcp_servers,omitempty"`

	// Distill, when *false, skips distillation entirely (matching MCP-tool
	// "distill: false" opt-out). Default (nil or *true) follows per-fragment
	// NoDistill flags.
	Distill   *bool     `json:"distill,omitempty"`
	Distiller Distiller `json:"-"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
}

// UpdateBundleResult reports what changed. Status is "updated" when at least
// one mutation took effect; otherwise "no_changes".
type UpdateBundleResult struct {
	Status  string   `json:"status"`
	Name    string   `json:"name"`
	Changes []string `json:"changes,omitempty"`
	Path    string   `json:"path,omitempty"`
}

// UpdateBundle mutates a bundle in place. Returns "no_changes" when the request
// produced no diff so callers can detect idempotent operations.
//
// Path safety: req.Name is validated via bundles.ValidateBundleName — see
// CreateBundle for the contract.
func UpdateBundle(ctx context.Context, cfg *config.Config, req UpdateBundleRequest) (*UpdateBundleResult, error) {
	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Name)
	if err != nil {
		return nil, err
	}

	var changes []string
	changes = applyScalarEdits(bundle, req, changes)

	// Tags reuse the shared list-edit primitive (applyListEdits, profiles.go);
	// fragments/prompts/MCP each have a dedicated helper because they merge into
	// maps and (for fragments/prompts) report which names need (re)distillation.
	bundle.Tags, changes = applyListEdits(bundle.Tags, req.AddTags, req.RemoveTags, "tag", changes)

	// Add* are create-if-absent: filter to names not already present, then reuse
	// the same merge path (for brand-new names a merge is identical to a set).
	var fragmentDistillTargets, promptDistillTargets []string
	changes, addFT := applyFragmentEdits(bundle, onlyNewKeys(req.AddFragments, bundle.Fragments), nil, changes)
	changes, fragmentDistillTargets = applyFragmentEdits(bundle, req.SetFragments, req.RemoveFragments, changes)
	fragmentDistillTargets = append(fragmentDistillTargets, addFT...)
	changes, addPT := applyPromptEdits(bundle, onlyNewKeys(req.AddPrompts, bundle.Commands), nil, changes)
	changes, promptDistillTargets = applyPromptEdits(bundle, req.SetPrompts, req.RemovePrompts, changes)
	promptDistillTargets = append(promptDistillTargets, addPT...)
	changes = applyMCPEdits(bundle, onlyNewKeys(req.AddMCPServers, bundle.MCP), nil, changes)
	changes = applyMCPEdits(bundle, req.SetMCPServers, req.RemoveMCPServers, changes)

	if len(changes) == 0 {
		return &UpdateBundleResult{Status: "no_changes", Name: req.Name, Path: bundle.Path}, nil
	}

	runUpdateDistill(ctx, bundle, req, fragmentDistillTargets, promptDistillTargets)

	if err := store.Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}

	return &UpdateBundleResult{
		Status:  "updated",
		Name:    req.Name,
		Changes: changes,
		Path:    bundle.Path,
	}, nil
}

// loadBundleForUpdate validates the request, loads the named bundle, and
// rejects symlinked paths (the loader resolves a name to whatever it finds on
// disk; a symlinked component or file would let Save escape the bundles tree).
func loadBundleForUpdate(store bundles.Store, cfg *config.Config, name string) (*bundles.Bundle, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if err := bundles.ValidateBundleName(name); err != nil {
		return nil, err
	}
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	bundle, err := store.Load(name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", name, err)
	}
	if err := requireSafeBundlePath(cfg.GetBundleDirs(), bundle.Path); err != nil {
		return nil, err
	}
	return bundle, nil
}

// ListBundles returns a summary of every bundle to display (ADR 0019: the read
// path lives here, not in the CLI): locally-authored bundles, remote bundles
// named canonically via the Resolver/VCS seam, and bundles removed upstream
// (flagged Deleted). See listBundleInfos for how the three sources are merged
// without double-listing.
//
// ctx is threaded rather than manufactured: the removed-upstream pass walks git
// history over every installed clone, which is the slow part of a listing and
// the part a user interrupting `bundle list` means to stop.
func ListBundles(ctx context.Context, cfg *config.Config) ([]*bundles.BundleInfo, error) {
	return listBundleInfos(ctx, cfg)
}

// GetBundle loads a single bundle by name. This is a READ path, so it goes
// through the seeded loader (like GetItemContent/GetBundleMCP in items.go):
// remote bundles exist only as lockfile-seeded references, and the unseeded
// store would report "not found" for every canonical ref ListBundles just
// displayed. Mutation paths (Update/Delete) keep the unseeded store — seeded
// bundles are read-only.
func GetBundle(cfg *config.Config, name string) (*bundles.Bundle, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	// A per-remote short "<remote>/<bundle>" arg resolves to the pinned remote
	// bundle's canonical key so the seeded loader finds it; a local file of the
	// same spelling wins (decision E). A bare/canonical name is unchanged.
	return bundleLoader(cfg).Load(canonicalizeBundleArg(cfg, name, cfg.GetBundleDirs(), nil))
}

// bundleStore returns the injected bundle store or the default filesystem
// adapter over the configured bundle dirs. Per ADR 0026 operations depends on
// the Store port; a frontend (or test) may inject an alternative, and the
// filesystem is the default when none is supplied — the same nil-default
// pattern as the profile loader and afero.Fs seams.
func bundleStore(cfg *config.Config, injected bundles.Store) bundles.Store {
	if injected != nil {
		return injected
	}
	// The SESSION's loader, not a private one. A store that resolved its own
	// sources held a second view of the same bundles, so a write through the
	// store and a read through cfg.BundleLoader() disagreed until something
	// re-read by luck. Sharing it means Save invalidates the very set every
	// other reader consults, and the earlier decorator that reconciled the two
	// caches from the outside is gone with the disjointness that required it.
	return bundles.NewStore(cfg.BundleLoader())
}

// applyScalarEdits applies the single-value description/version edits, appending
// a change line for each that was set.
func applyScalarEdits(bundle *bundles.Bundle, req UpdateBundleRequest, changes []string) []string {
	if req.SetDescription != nil {
		bundle.Description = *req.SetDescription
		changes = append(changes, "updated description")
	}
	if req.SetVersion != nil {
		bundle.Version = *req.SetVersion
		changes = append(changes, "updated version")
	}
	return changes
}

// runUpdateDistill (re)distills the queued fragment/prompt targets unless the
// request opts out wholesale (Distill: false). Targets are sorted for
// deterministic distill ordering.
func runUpdateDistill(ctx context.Context, bundle *bundles.Bundle, req UpdateBundleRequest, fragmentTargets, promptTargets []string) {
	if req.Distill != nil && !*req.Distill {
		return
	}
	sort.Strings(fragmentTargets)
	sort.Strings(promptTargets)
	distillFragments(ctx, bundle, fragmentTargets, req.Distiller)
	distillPrompts(ctx, bundle, promptTargets, req.Distiller)
}

// applyFragmentEdits merges set inputs into the bundle's fragments and applies
// removals, appending a change line per mutation. A set input that merges to an
// entry identical to the existing one is NOT a mutation and reports nothing:
// UpdateBundle's "no_changes" status is defined as "the request produced no
// diff", so a re-applied identical edit must not claim one. It returns the names that need
// (re)distillation: a fragment is queued when it is new or its content actually
// changed (unless NoDistill). A metadata-only edit (same content) updates
// tags/notes/installation but preserves the cached Distilled/DistilledBy/
// ContentHash and does NOT queue — re-distilling would burn tokens for no
// semantic change.
func applyFragmentEdits(bundle *bundles.Bundle, set map[string]BundleFragmentInput, remove []string, changes []string) (newChanges, distillTargets []string) {
	for name, in := range set {
		if bundle.Fragments == nil {
			bundle.Fragments = make(map[string]bundles.BundleFragment)
		}
		existing, hadExisting := bundle.Fragments[name]
		merged := existing
		merged.Tags = in.Tags
		merged.Notes = in.Notes
		merged.Installation = in.Installation
		merged.NoDistill = in.NoDistill
		merged.Premise = in.Premise
		if !hadExisting || existing.Content != in.Content {
			merged.Content = in.Content
			merged.Distilled = ""
			merged.DistilledBy = ""
			merged.ContentHash = ""
			if !in.NoDistill {
				distillTargets = append(distillTargets, name)
			}
		}
		if hadExisting && reflect.DeepEqual(merged, existing) {
			continue
		}
		bundle.Fragments[name] = merged
		changes = append(changes, "set fragment: "+name)
	}
	for _, name := range remove {
		if _, ok := bundle.Fragments[name]; ok {
			delete(bundle.Fragments, name)
			changes = append(changes, "removed fragment: "+name)
		}
	}
	return changes, distillTargets
}

// applyPromptEdits is the prompt counterpart of applyFragmentEdits, with the
// same content-aware (re)distillation rule plus a Description field.
func applyPromptEdits(bundle *bundles.Bundle, set map[string]BundleCommandInput, remove []string, changes []string) (newChanges, distillTargets []string) {
	for name, in := range set {
		if bundle.Commands == nil {
			bundle.Commands = make(map[string]bundles.BundleCommand)
		}
		existing, hadExisting := bundle.Commands[name]
		merged := existing
		merged.Description = in.Description
		merged.Tags = in.Tags
		merged.Notes = in.Notes
		merged.Installation = in.Installation
		merged.NoDistill = in.NoDistill
		if !hadExisting || existing.Content != in.Content {
			merged.Content = in.Content
			merged.Distilled = ""
			merged.DistilledBy = ""
			merged.ContentHash = ""
			if !in.NoDistill {
				distillTargets = append(distillTargets, name)
			}
		}
		if hadExisting && reflect.DeepEqual(merged, existing) {
			continue
		}
		bundle.Commands[name] = merged
		changes = append(changes, "set prompt: "+name)
	}
	for _, name := range remove {
		if _, ok := bundle.Commands[name]; ok {
			delete(bundle.Commands, name)
			changes = append(changes, "removed prompt: "+name)
		}
	}
	return changes, distillTargets
}

// onlyNewKeys returns the subset of in whose keys are absent from existing —
// the add-only filter so create-if-absent never overwrites. The value types of
// the two maps are independent: in carries the edit inputs, existing only gates
// on key presence.
func onlyNewKeys[V, E any](in map[string]V, existing map[string]E) map[string]V {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]V, len(in))
	for name, v := range in {
		if _, exists := existing[name]; !exists {
			out[name] = v
		}
	}
	return out
}

// applyMCPEdits merges set inputs into the bundle's MCP servers and applies
// removals, appending a change line per mutation — a merge identical to the
// existing entry is not one, per applyFragmentEdits. MCP servers carry no
// distilled content, so there are no distill targets to return.
func applyMCPEdits(bundle *bundles.Bundle, set map[string]BundleMCPInput, remove []string, changes []string) []string {
	for name, in := range set {
		if bundle.MCP == nil {
			bundle.MCP = make(map[string]bundles.BundleMCP)
		}
		existing, hadExisting := bundle.MCP[name]
		merged := existing
		merged.Command = in.Command
		merged.Args = in.Args
		merged.Env = in.Env
		merged.Notes = in.Notes
		merged.Installation = in.Installation
		if hadExisting && reflect.DeepEqual(merged, existing) {
			continue
		}
		bundle.MCP[name] = merged
		changes = append(changes, "set mcp: "+name)
	}
	for _, name := range remove {
		if _, ok := bundle.MCP[name]; ok {
			delete(bundle.MCP, name)
			changes = append(changes, "removed mcp: "+name)
		}
	}
	return changes
}

// DeleteBundleRequest is the input for DeleteBundle.
type DeleteBundleRequest struct {
	Name string `json:"name"`

	// Store, when non-nil, is the bundle storage adapter (ADR 0026); nil
	// defaults to the filesystem.
	Store bundles.Store `json:"-"`
}

// DeleteBundleResult reports the path that was removed.
type DeleteBundleResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// DeleteBundle removes the bundle file from disk. Returns "not found" if the
// bundle isn't installed.
//
// Path safety: req.Name is validated via bundles.ValidateBundleName before
// the bundle is looked up, so AI-supplied names can't address files outside
// the configured bundle search dirs. See CreateBundle for the contract.
func DeleteBundle(_ context.Context, cfg *config.Config, req DeleteBundleRequest) (*DeleteBundleResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if err := bundles.ValidateBundleName(req.Name); err != nil {
		return nil, err
	}
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	store := bundleStore(cfg, req.Store)
	bundle, err := store.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	// Delete on a symlink removes the link (not its target), so delete
	// is intrinsically safer than write — but a symlinked parent could
	// still steer the loader at a file outside the bundles tree, so apply
	// the same check Update uses.
	if err := requireSafeBundlePath(cfg.GetBundleDirs(), bundle.Path); err != nil {
		return nil, err
	}

	if err := store.Delete(req.Name); err != nil {
		return nil, fmt.Errorf("failed to remove bundle: %w", err)
	}

	return &DeleteBundleResult{Status: "deleted", Name: req.Name, Path: bundle.Path}, nil
}

// PushBundleRequest is the input for PushBundle: a bundle file path and the
// resolved remote to publish it to. PushBundle does no remote inference — the
// caller resolves Remote first with ResolveBundleRemote, so this operation stays
// a single responsibility: validate the file and publish it to a named remote.
type PushBundleRequest struct {
	Path string `json:"path"`
	// Remote is the resolved registry name to publish to. Required.
	Remote string `json:"remote"`
	// Title is the PR title (and commit subject). Kept separate from Message
	// because GitHub PR titles cap at 256 bytes — collapsing a long commit
	// message into the title silently truncates body detail.
	Title    string `json:"title,omitempty"`
	Message  string `json:"message,omitempty"`
	CreatePR bool   `json:"create_pr,omitempty"`
	DryRun   bool   `json:"dry_run,omitempty"`

	// PublishManager overrides the default registry-built one. Tests inject
	// a manager backed by a mock Publisher; production callers leave this
	// nil so a real network-backed manager is constructed from cfg.
	PublishManager *remote.PublishManager `json:"-"`

	// A bundle's signature is not a publish-time concern: `ctxloom bundle
	// sign` writes a tree's SHA256SUMS manifest and its .sigs/ entry, and a
	// tree push carries that store with the rest of the tree.
}

// PushBundleResult reports what was (or would be) published.
type PushBundleResult struct {
	Status     string `json:"status"` // "preview" (dry-run), "pushed", "pr-created"
	Path       string `json:"path"`
	Remote     string `json:"remote"`      // Resolved registry name, or URL if no name match.
	TargetPath string `json:"target_path"` // Path inside the remote repo (e.g. .ctxloom/content/bundles/v2/foo).
	Branch     string `json:"branch,omitempty"`
	Title      string `json:"title,omitempty"`   // PR title / commit subject
	Message    string `json:"message,omitempty"` // PR body / commit body
	CreatePR   bool   `json:"create_pr"`
	SizeBytes  int    `json:"size_bytes,omitempty"`

	// Set on real push (non-dry-run).
	CommitSHA string `json:"commit_sha,omitempty"`
	PRURL     string `json:"pr_url,omitempty"`

	// Set on dry-run only — human-readable summary of what would happen.
	Preview string `json:"preview,omitempty"`

	// Signed reports whether the published tree carries a signature — an
	// entry in its .sigs/ store.
	Signed bool `json:"signed,omitempty"`
}

// PushBundle publishes (or dry-runs) a local bundle tree, named by its
// bundle.yaml, to req.Remote (a resolved registry name — see
// ResolveBundleRemote). Network is touched only for a non-dry-run.
func PushBundle(ctx context.Context, cfg *config.Config, req PushBundleRequest) (*PushBundleResult, error) {
	if err := validatePushRequest(cfg, req); err != nil {
		return nil, err
	}

	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	if filepath.Base(absPath) != bundles.DirectoryFormManifest {
		return nil, fmt.Errorf("%w: %s", ErrNotABundleTree, absPath)
	}

	// Read + parse the bundle to validate it before any inference. Push has no
	// FS seam by design (a remote move publishes through here and reads the real
	// filesystem), so the OS filesystem is stated rather than injected. Only the
	// BYTES are kept: what publishes is the document verbatim (spec §3.0), so the
	// parse is a gate here and never a source of values.
	data, _, err := bundles.EnvelopeAt(afero.NewOsFs(), absPath)
	if errors.Is(err, bundles.ErrEnvelopeRead) {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	// Checked on the BYTES, ahead of the parse verdict, so a file with nothing
	// in it is named for what it is. ParseBundle refuses this document too
	// (Bundle.declaresNothing), but as a parse failure — and someone who
	// truncated a file is served by "the file has no content", not by a sentence
	// about YAML. Publishing it would overwrite whatever is at the remote path.
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("refusing to publish empty bundle %s: the file has no content", absPath)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid bundle: %w", err)
	}

	// The last gate before bytes leave the machine: a tree whose manifest no
	// longer covers its files is refused (the reader established that fact),
	// because publishing it hands every consumer a hard tamper alarm over
	// content that was never attacked — just edited after it was signed.
	// Fail-closed: never a quiet downgrade to unsigned.
	if err := refusePushOfStaleTree(cfg, absPath); err != nil {
		return nil, err
	}

	registry, err := getRegistry(cfg)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	rem, err := registry.Get(req.Remote)
	if err != nil {
		return nil, fmt.Errorf("remote %q not found: %w", req.Remote, err)
	}

	// Bundle name in the target repo = the name the LOADER knows this bundle by,
	// not the basename of the file it happens to live in, which is always
	// "bundle.yaml" — publishing by basename made every bundle publish as
	// "bundle", collide at one remote path, and silently overwrite the last one.
	// ExtractBundleName is the loader's own rule (bundles.Loader sets
	// Bundle.Name from it), so what you push is filed under the name you pushed.
	//
	// The path is computed ONCE, here, and then both reported (result.TargetPath
	// — printed by the CLI, and turned into SigDest by moveToRemote) and written
	// (handed to publish as PublishOptions.RemotePath by runPush), so the two
	// cannot disagree.
	bundleName := bundles.ExtractBundleName(absPath)
	targetPath := remote.PublishPath(remote.ItemTypeBundle, bundleName)

	// Resolve title/body the same way publish.go does, so the result accurately
	// reflects what the PR will look like (title may be lifted from message).
	title, message := resolvePushTitle(req, bundleName)

	result := &PushBundleResult{
		Path:       absPath,
		Remote:     req.Remote,
		TargetPath: targetPath,
		Title:      title,
		Message:    message,
		CreatePR:   req.CreatePR,
		SizeBytes:  len(data),
	}

	if req.DryRun {
		result.Status = "preview"
		result.Preview = pushDryRunPreview(bundleName, len(data), rem.URL, targetPath, req.CreatePR, title)
		return result, nil
	}

	return runPush(ctx, cfg, registry, req.Remote, absPath, req, result)
}

// validatePushRequest checks the request preconditions for PushBundle.
func validatePushRequest(cfg *config.Config, req PushBundleRequest) error {
	if req.Path == "" {
		return fmt.Errorf("path is required")
	}
	if req.Remote == "" {
		return fmt.Errorf("remote is required (resolve it with ResolveBundleRemote)")
	}
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return fmt.Errorf("no .ctxloom directory configured")
	}
	return nil
}

// resolvePushTitle resolves the commit subject and body for a push, lifting the
// title from the message when unset (mirroring publish.go) and falling back to
// a generated default.
func resolvePushTitle(req PushBundleRequest, bundleName string) (title, message string) {
	title, message = remote.SplitTitleBody(req.Title, req.Message)
	if title == "" {
		title = fmt.Sprintf("Update bundle: %s", bundleName)
	}
	return title, message
}

// pushDryRunPreview renders the human-readable summary of a dry-run push.
func pushDryRunPreview(bundleName string, size int, remURL, targetPath string, createPR bool, title string) string {
	action := "direct push"
	if createPR {
		action = "pull request"
	}
	return fmt.Sprintf("Would publish %s (%d bytes) to %s as %s via %s; title: %q",
		bundleName, size, remURL, targetPath, action, title)
}

// runPush performs the actual (non-dry-run) publish and records the outcome
// on result. The tree travels through PublishTree, so that every file under
// the bundle's directory lands in ONE commit (engaged-chivalry).
//
// A tree's signature — its SHA256SUMS manifest and .sigs/ entries, written to
// disk before PushBundle runs by `ctxloom bundle sign` — is just more of the
// files gatherPublishTreeFiles walks off disk, so it travels in that same
// commit and result.Signed reports whether one was carried.
func runPush(ctx context.Context, cfg *config.Config, registry *remote.Registry, remoteName, absPath string, req PushBundleRequest, result *PushBundleResult) (*PushBundleResult, error) {
	pm := req.PublishManager
	if pm == nil {
		pm = remote.NewPublishManager(registry, remote.LoadAuth(cfg.GetAppPaths()[0]))
	}

	opts := remote.PublishOptions{
		CreatePR: req.CreatePR,
		Title:    result.Title,
		Message:  result.Message,
		ItemType: remote.ItemTypeBundle,
		// The reported destination IS the published destination: one value,
		// computed in PushBundle, handed to publish rather than recomputed here.
		RemotePath: result.TargetPath,
	}

	files, err := gatherPublishTreeFiles(afero.NewOsFs(), filepath.Dir(absPath))
	if err != nil {
		return nil, fmt.Errorf("gather bundle tree: %w", err)
	}
	pubResult, err := pm.PublishTree(ctx, files, remoteName, opts)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}

	result.CommitSHA = pubResult.SHA
	result.PRURL = pubResult.PRURL
	result.Signed = treeCarriesSignature(files)
	result.Status = "pushed"
	if req.CreatePR {
		result.Status = "pr-created"
	}
	return result, nil
}

// gatherPublishTreeFiles walks the directory holding a bundle's manifest and
// returns every file's bytes keyed by its path relative to that directory,
// forward-slash normalized — the shape remote.PublishManager.PublishTree
// wants for its per-file remote paths.
//
// Every file travels, with no filter — the same rule bundle_transfer.go's
// copyBundleTree states for export: a bundle proves its own integrity with a
// SHA256SUMS covering the whole tree (or, for the simpler manifest-only
// sidecar this format still uses, the manifest bytes alone), so silently
// dropping one file here would make a consumer see tampering rather than an
// interrupted publish.
func gatherPublishTreeFiles(afs afero.Fs, dir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := afero.Walk(afs, dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", p, err)
		}
		data, err := afero.ReadFile(afs, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ResolveBundleRemote decides which configured remote a bundle file publishes
// to. An explicit override wins (after validating it exists); otherwise the
// remote is inferred from the bundle's location — a cache path or enclosing git
// origin, then the registry default, then a sole configured remote, else an
// ambiguous-remote error listing the candidates. Both the CLI and the VSCode
// companion call this before PushBundle, so neither re-implements the inference.
func ResolveBundleRemote(cfg *config.Config, bundlePath, override string) (string, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return "", fmt.Errorf("no .ctxloom directory configured")
	}
	absPath, err := filepath.Abs(bundlePath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	registry, err := getRegistry(cfg)
	if err != nil {
		return "", fmt.Errorf("load registry: %w", err)
	}
	if override != "" {
		if _, err := registry.Get(override); err != nil {
			return "", fmt.Errorf("remote %q not found: %w", override, err)
		}
		return override, nil
	}
	return resolveRemoteForPath(cfg, registry, absPath)
}

// resolveRemoteForPath implements the location-based inference used by
// ResolveBundleRemote. Returns the remote name to publish to.
func resolveRemoteForPath(cfg *config.Config, registry *remote.Registry, absPath string) (string, error) {
	// (1) Path under cache/bundles/<remote>/<rest>.yaml. Authored bundles no
	// longer live here (they're under content/bundles, which encodes no
	// remote), so this only still fires for legacy cache artifacts.
	if name, ok := remoteFromCachePath(cfg, registry, absPath); ok {
		return name, nil
	}

	// (2) Path outside .ctxloom in a git tree — match its origin URL.
	if name, ok := remoteFromGitOrigin(cfg, registry, absPath); ok {
		return name, nil
	}

	// (3) Registry default.
	if def := registry.GetDefault(); def != "" {
		return def, nil
	}

	// (4) Exactly one configured remote.
	all := registry.List()
	if len(all) == 1 {
		return all[0].Name, nil
	}

	// (5) Ambiguous — surface candidates so the user can pick.
	if len(all) > 1 {
		return "", ambiguousRemoteError(all)
	}

	return "", fmt.Errorf("no remote configured: add one with `ctxloom remote add`")
}

// remoteFromCachePath resolves the remote from a path under one layout's cache
// subtree, cache/bundles/<layout>/<remote>/<rest>, requiring the first segment
// BELOW that subtree to be a known remote.
//
// It probes each layout rather than the bare cache/bundles root, and that is
// load-bearing: an install lands under CacheBundlesPathFor, so relative to the
// bare root the first segment is the LAYOUT ("v2"), which is never a registered
// remote — the match would fail for every bundle the current code installs.
func remoteFromCachePath(cfg *config.Config, registry *remote.Registry, absPath string) (string, bool) {
	app := cfg.GetAppPaths()[0]
	for _, l := range []paths.BundleLayout{paths.LayoutV2} {
		rel, err := filepath.Rel(paths.CacheBundlesPathFor(app, l), absPath)
		if err != nil || isOutsideRel(rel) {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) >= 2 && registry.Has(parts[0]) {
			return parts[0], true
		}
	}
	return "", false
}

// remoteFromGitOrigin resolves the remote by matching a path's git origin URL
// against the registry. Only paths OUTSIDE .ctxloom qualify: a bundle inside
// .ctxloom/cache might be in the project's git tree, but that doesn't tell us
// where to push (the project repo is rarely a bundles repo).
func remoteFromGitOrigin(cfg *config.Config, registry *remote.Registry, absPath string) (string, bool) {
	if isUnderCtxloom(cfg, absPath) {
		return "", false
	}
	originURL, err := gitutil.GetRemoteURL(absPath, "origin")
	if err != nil {
		return "", false
	}
	if name := matchRemoteByURL(registry, originURL); name != "" {
		return name, true
	}
	return "", false
}

// ambiguousRemoteError reports the candidate remotes (sorted) when no single
// remote can be inferred. Authored bundles live under content/bundles, which
// carries no remote in its path (unlike the legacy cache/bundles/<remote>/
// layout), so the fix is to say which remote explicitly rather than move the
// bundle anywhere.
func ambiguousRemoteError(all []*remote.Remote) error {
	names := make([]string, 0, len(all))
	for _, r := range all {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return fmt.Errorf("ambiguous remote: run `ctxloom bundle push <name> <remote>` to name one, `ctxloom remote default <remote>` to set a default, or `ctxloom bundle move <name> --to <remote>` to relocate the bundle. Candidates: %s",
		strings.Join(names, ", "))
}

// isUnderCtxloom reports whether absPath lives under cfg's .ctxloom directory.
func isUnderCtxloom(cfg *config.Config, absPath string) bool {
	for _, app := range cfg.GetAppPaths() {
		appAbs, err := filepath.Abs(app)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(appAbs, absPath)
		if err == nil && !isOutsideRel(rel) && rel != "." {
			return true
		}
	}
	return false
}

// isOutsideRel reports whether a result from filepath.Rel escapes the base
// directory. Naive `strings.HasPrefix(rel, "..")` misclassifies legitimate
// names like "..hidden"; we need the separator to follow.
func isOutsideRel(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// requireSafeBundlePath verifies that target lives under one of the given
// bundle dirs AND that no directory component between the containing dir
// and target (inclusive of target itself when it exists) is a symlink.
// This closes the symlink-traversal vector that ValidateBundleName alone
// cannot: a pre-existing symlink inside the bundles tree could otherwise
// steer os.WriteFile to clobber files outside it.
//
// Bundle roots themselves may be symlinks (a legitimate user choice — e.g.,
// ~/.ctxloom symlinked into a workspace). Only components below a root
// are rejected. Non-existent trailing components are fine; they'll be
// created by MkdirAll as real directories after this check passes.
//
// Returns an error if target is not under any of dirs, or if the walk hits
// a symlink, or if Lstat fails for an unexpected reason.
func requireSafeBundlePath(dirs []string, target string) error {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	for _, dir := range dirs {
		if matched, err := bundlePathUnderDir(dir, absTarget); matched {
			return err
		}
	}
	return fmt.Errorf("bundle path %s not under any configured bundles directory", target)
}

// bundlePathUnderDir reports whether absTarget is under dir; when it is
// (matched=true), err reports whether the path is safe (no symlink traversal).
// matched=false means the caller should try the next directory.
func bundlePathUnderDir(dir, absTarget string) (bool, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false, nil
	}
	rel, err := filepath.Rel(absDir, absTarget)
	if err != nil || isOutsideRel(rel) {
		return false, nil
	}
	if rel == "." {
		return true, nil
	}
	return true, checkNoSymlinkTraversal(absDir, rel)
}

// checkNoSymlinkTraversal walks each component of rel under absDir, rejecting
// any symlink. Non-existent trailing components are fine (MkdirAll will create
// them as real directories after this check).
func checkNoSymlinkTraversal(absDir, rel string) error {
	cur := absDir
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("lstat %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid bundle path: %s is a symlink (traversal not allowed)", cur)
		}
	}
	return nil
}

// matchRemoteByURL finds a registry entry whose URL matches the given URL
// (after normalization). Returns "" if no match.
func matchRemoteByURL(registry *remote.Registry, url string) string {
	for _, r := range registry.List() {
		if remote.SameRepository(r.URL, url) {
			return r.Name
		}
	}
	return ""
}

// applyInputs writes converted inputs into one of the bundle's item maps,
// allocating it on first use. Distillation is left to the caller
// (CreateBundle/UpdateBundle invoke the distiller after this). conv is the only
// per-kind part: it maps one input DTO to its stored entry.
func applyInputs[I, E any](dst *map[string]E, in map[string]I, conv func(I) E) {
	if len(in) == 0 {
		return
	}
	if *dst == nil {
		*dst = make(map[string]E, len(in))
	}
	for name, item := range in {
		(*dst)[name] = conv(item)
	}
}

func applyFragmentInputs(b *bundles.Bundle, in map[string]BundleFragmentInput) {
	applyInputs(&b.Fragments, in, func(frag BundleFragmentInput) bundles.BundleFragment {
		return bundles.BundleFragment{
			ItemBody: bundles.ItemBody{
				Tags:         frag.Tags,
				Notes:        frag.Notes,
				Installation: frag.Installation,
				Content:      frag.Content,
				NoDistill:    frag.NoDistill,
			},
			Premise: frag.Premise,
		}
	})
}

func applyPromptInputs(b *bundles.Bundle, in map[string]BundleCommandInput) {
	applyInputs(&b.Commands, in, func(p BundleCommandInput) bundles.BundleCommand {
		return bundles.BundleCommand{
			ItemBody: bundles.ItemBody{
				Tags:         p.Tags,
				Notes:        p.Notes,
				Installation: p.Installation,
				Content:      p.Content,
				NoDistill:    p.NoDistill,
			},
			Description: p.Description,
		}
	})
}

func applyMCPInputs(b *bundles.Bundle, in map[string]BundleMCPInput) {
	applyInputs(&b.MCP, in, func(m BundleMCPInput) bundles.BundleMCP {
		return bundles.BundleMCP{
			Command:      m.Command,
			Args:         m.Args,
			Env:          m.Env,
			Notes:        m.Notes,
			Installation: m.Installation,
		}
	})
}

// namesNeedingDistill picks the input names that should be (re)distilled: those
// whose NoDistill is unset AND that actually landed in the bundle (present).
// Deterministic order keeps tests stable and distillation reproducible.
func namesNeedingDistill[I, E any](present map[string]E, in map[string]I, noDistill func(I) bool) []string {
	if len(in) == 0 {
		return nil
	}
	names := make([]string, 0, len(in))
	for name, item := range in {
		if noDistill(item) {
			continue
		}
		if _, ok := present[name]; !ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func namesNeedingFragmentDistill(b *bundles.Bundle, in map[string]BundleFragmentInput) []string {
	return namesNeedingDistill(b.Fragments, in, func(f BundleFragmentInput) bool { return f.NoDistill })
}

func namesNeedingPromptDistill(b *bundles.Bundle, in map[string]BundleCommandInput) []string {
	return namesNeedingDistill(b.Commands, in, func(p BundleCommandInput) bool { return p.NoDistill })
}

// distillFragments invokes the Distiller for each named fragment, populating
// Distilled / DistilledBy / ContentHash on success. Distill errors are warned
// but non-fatal: the bundle still saves with the raw content (fault-tolerance
// philosophy in CLAUDE.md). A nil Distiller is a no-op. The returned set names
// the items whose attempt FAILED, and is the ONLY reliable signal of failure:
// post-state cannot be read as one, because what a failure leaves behind
// differs by caller. On the DistillBundleFile path the item is read straight
// off disk, so a failed re-distill leaves the previous Distilled/DistilledBy
// standing. On the CreateBundle/UpdateBundle path it does not: applyFragment
// Edits has already blanked Distilled/DistilledBy/ContentHash for every item
// whose content changed (a summary of superseded content is worse than none),
// so a failure there saves the item with raw content and no distillation at
// all. Callers must branch on the returned set, never on the fields.
// Distillation floor: distillFragments/distillPrompts previously
// stamped ANY non-empty Distiller result as accepted — no length, ratio, or
// sanity floor — so a 16-byte distillation of a 1,428-byte fragment (a
// truncated or degenerate model response) was indistinguishable from a real
// summary, and resolveEffective serves it whenever it is non-empty. The
// empty-string case is already self-healing (staleDistill treats "" as
// never-distilled and re-queues it); this floor catches the truncation case
// that slips past that check. minDistillAbsoluteBytes rejects a distillation
// too short to be any summary at all, regardless of the source's size;
// minDistillRatio additionally rejects one that kept less than this fraction
// of a LARGER source, which the absolute floor alone would not catch.
const (
	minDistillAbsoluteBytes = 4
	minDistillRatio         = 0.02
)

// distillTooShort reports whether distilled is too small, relative to
// original, to plausibly be a real distillation rather than a truncated or
// degenerate model response.
func distillTooShort(original, distilled string) bool {
	if len(distilled) < minDistillAbsoluteBytes {
		return true
	}
	if len(original) == 0 {
		return false
	}
	return float64(len(distilled))/float64(len(original)) < minDistillRatio
}

// distillItems is the one implementation of the distillation policy, shared by
// distillFragments and distillPrompts. Only the item type differs between them,
// and the policy — what counts as a failed attempt, what is written on success —
// is safety-relevant enough that a second copy is a liability: the two copies
// had already drifted in their explanatory comments, which is how they drift in
// behaviour next.
//
// get returns the named entry and its raw content; put applies an ACCEPTED
// result. Neither is called for a rejected one, so no rejection path can stamp
// a hash.
func distillItems[E any](
	ctx context.Context,
	b *bundles.Bundle,
	names []string,
	d Distiller,
	kind DistillKind,
	noun string,
	get func(name string) (entry E, content string),
	put func(name string, entry E, res DistillResult),
) collections.Set[string] {
	failed := collections.NewSet[string]()
	if d == nil || len(names) == 0 {
		return failed
	}
	for _, name := range names {
		entry, content := get(name)
		res, err := d.Distill(ctx, DistillRequest{
			Kind:    kind,
			Name:    name,
			Content: content,
			Bundle:  b,
		})
		if err != nil {
			clidiag.Warn("ctxloom", "distill of %s %q failed: %v", noun, name, err)
			failed.Add(name)
			continue
		}
		if res.Distilled == "" {
			// Zero bytes delivered is a FAILED distillation, not a successful
			// one that happened to be empty. Assigning it would overwrite a
			// previously-good distillation with "" and let distillOutcome
			// report "distilled" for content nobody can use.
			clidiag.Warn("ctxloom", "distill of %s %q produced no content; keeping the previous distillation", noun, name)
			failed.Add(name)
			continue
		}
		if distillTooShort(content, res.Distilled) {
			// Non-empty but implausibly short (truncated/degenerate) is the same
			// class of failure as empty — treat it the same way, and do NOT stamp
			// ContentHash for a rejected result.
			clidiag.Warn("ctxloom", "distill of %s %q produced only %d bytes from %d — rejecting as truncated, keeping the previous distillation", noun, name, len(res.Distilled), len(content))
			failed.Add(name)
			continue
		}
		put(name, entry, res)
	}
	return failed
}

func distillFragments(ctx context.Context, b *bundles.Bundle, names []string, d Distiller) collections.Set[string] {
	return distillItems(ctx, b, names, d, DistillKindFragment, "fragment",
		func(name string) (bundles.BundleFragment, string) {
			frag := b.Fragments[name]
			return frag, frag.Content
		},
		func(name string, frag bundles.BundleFragment, res DistillResult) {
			frag.Distilled = res.Distilled
			frag.DistilledBy = res.ModelID
			frag.ContentHash = frag.ComputeContentHash()
			b.Fragments[name] = frag
		})
}

// distillPrompts mirrors distillFragments for prompts.
func distillPrompts(ctx context.Context, b *bundles.Bundle, names []string, d Distiller) collections.Set[string] {
	return distillItems(ctx, b, names, d, DistillKindCommand, "prompt",
		func(name string) (bundles.BundleCommand, string) {
			p := b.Commands[name]
			return p, p.Content
		},
		func(name string, p bundles.BundleCommand, res DistillResult) {
			p.Distilled = res.Distilled
			p.DistilledBy = res.ModelID
			p.ContentHash = p.ComputeContentHash()
			b.Commands[name] = p
		})
}

// treeCarriesSignature reports whether a walked tree carries a signature:
// an entry in its .sigs/ store.
func treeCarriesSignature(files map[string][]byte) bool {
	for rel := range files {
		if strings.HasPrefix(filepath.ToSlash(rel), content.SigDirName+"/") {
			return true
		}
	}
	return false
}

// refusePushOfStaleTree refuses to publish a bundle whose signature no longer
// covers its files, by the reader's own facts (refuseStaleSignature over the
// project's bundle roots). A tree outside the project's bundle roots has no
// reader to ask and publishes as it stands.
func refusePushOfStaleTree(cfg *config.Config, absPath string) error {
	if cfg == nil {
		return nil
	}
	name := filepath.Base(filepath.Dir(absPath))
	read, err := bundles.NewLoader(projectReader(afero.NewOsFs(), cfg.BundleReaderDirs())).Read(name)
	if err != nil || filepath.Clean(read.Bundle.Path) != filepath.Clean(absPath) {
		return nil
	}
	if read.Signature() != bundles.SignatureInvalid {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrStaleSignature, bundles.StaleSignatureAdvice(read))
}
