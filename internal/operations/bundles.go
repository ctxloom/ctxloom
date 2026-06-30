// Package operations: bundle authoring.
//
// CreateBundle/UpdateBundle/DeleteBundle/PushBundle expose the same authoring
// surface as `cmd/bundle.go` but in a Go-callable form usable from the MCP
// server. The CLI predates this layer; refactoring it to delegate here is a
// follow-up.
package operations

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/shared/clidiag"
	"github.com/ctxloom/shared/collections"
	"github.com/ctxloom/shared/gitutil"
)

// DistillKind tags whether a Distill call is for a fragment or a prompt.
// The real LLM-backed distiller uses this to scope the sibling-context it
// builds (excluding the item being distilled).
type DistillKind string

const (
	DistillKindFragment DistillKind = "fragment"
	DistillKindSkill    DistillKind = "skill"
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
	Skills      map[string]BundleSkillInput    `json:"skills,omitempty"`
	MCPServers  map[string]BundleMCPInput      `json:"mcp_servers,omitempty"`

	// Distiller, when non-nil, is invoked for each new fragment whose
	// NoDistill flag is unset. Distill failures are warned to stderr but
	// do not fail the create (fault-tolerance principle).
	Distiller Distiller `json:"-"`

	// Store, when non-nil, is the bundle storage adapter to persist through
	// (ADR 0026); nil defaults to the filesystem. Frontends leave it nil.
	Store bundles.Store `json:"-"`
}

// BundleFragmentInput describes a fragment to add or update via operations.
// Distillation is governed by NoDistill plus the request-level distill toggle
// in UpdateBundleRequest (see L3/L4 tests). BundleFragment has no Description
// field; use Notes for human-readable annotations not sent to the AI and
// Installation for setup instructions surfaced on install.
type BundleFragmentInput struct {
	Content      string   `json:"content"`
	Tags         []string `json:"tags,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	Installation string   `json:"installation,omitempty"`
	NoDistill    bool     `json:"no_distill,omitempty"`
}

// BundleSkillInput describes a prompt to add or update via operations.
type BundleSkillInput struct {
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

// CreateBundle writes a new bundle YAML to .ctxloom/cache/bundles/<name>.yaml.
//
// Path safety: req.Name is validated via bundles.ValidateBundleName before
// any filesystem call, so names containing "..", absolute paths, or null
// bytes are rejected. Slash-separated names like "personal/foo" are
// supported and land at .ctxloom/cache/bundles/personal/foo.yaml (parent
// dirs are MkdirAll'd). In addition, requireSafeBundlePath rejects any
// directory component already on disk that is a symlink — without this an
// attacker who had planted .ctxloom/cache/bundles/personal -> /etc could
// induce Save to write outside the bundles root.
func CreateBundle(ctx context.Context, cfg *config.Config, req CreateBundleRequest) (*CreateBundleResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if err := bundles.ValidateBundleName(req.Name); err != nil {
		return nil, err
	}
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	dir := paths.BundlesPath(cfg.AppPaths[0])
	path := filepath.Join(dir, req.Name+".yaml")
	if err := requireSafeBundlePath([]string{dir}, path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("bundle already exists: %s", path)
	}

	version := req.Version
	if version == "" {
		version = "1.0.0"
	}

	bundle := &bundles.Bundle{
		Version:     version,
		Description: req.Description,
		Tags:        req.Tags,
		Author:      req.Author,
		Path:        path,
	}
	applyFragmentInputs(bundle, req.Fragments)
	applyPromptInputs(bundle, req.Skills)
	applyMCPInputs(bundle, req.MCPServers)

	distillFragments(ctx, bundle, namesNeedingFragmentDistill(bundle, req.Fragments), req.Distiller)
	distillPrompts(ctx, bundle, namesNeedingPromptDistill(bundle, req.Skills), req.Distiller)

	if err := bundleStore(cfg, req.Store).Save(bundle); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}

	return &CreateBundleResult{
		Status: "created",
		Name:   req.Name,
		Path:   path,
	}, nil
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

	AddPrompts    map[string]BundleSkillInput `json:"add_prompts,omitempty"`
	SetPrompts    map[string]BundleSkillInput `json:"set_prompts,omitempty"`
	RemovePrompts []string                    `json:"remove_prompts,omitempty"`

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
	changes, addFT := applyFragmentEdits(bundle, onlyNewFragments(bundle, req.AddFragments), nil, changes)
	changes, fragmentDistillTargets = applyFragmentEdits(bundle, req.SetFragments, req.RemoveFragments, changes)
	fragmentDistillTargets = append(fragmentDistillTargets, addFT...)
	changes, addPT := applyPromptEdits(bundle, onlyNewPrompts(bundle, req.AddPrompts), nil, changes)
	changes, promptDistillTargets = applyPromptEdits(bundle, req.SetPrompts, req.RemovePrompts, changes)
	promptDistillTargets = append(promptDistillTargets, addPT...)
	changes = applyMCPEdits(bundle, onlyNewMCP(bundle, req.AddMCPServers), nil, changes)
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
	if cfg == nil || len(cfg.AppPaths) == 0 {
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
func ListBundles(cfg *config.Config) ([]*bundles.BundleInfo, error) {
	return listBundleInfos(context.Background(), cfg)
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
	return bundleLoader(cfg).Load(name)
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
	return bundles.NewFSStore(cfg.GetBundleDirs(), false)
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
// removals, appending a change line per mutation. It returns the names that need
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
		if !hadExisting || existing.Content != in.Content {
			merged.Content = in.Content
			merged.Distilled = ""
			merged.DistilledBy = ""
			merged.ContentHash = ""
			if !in.NoDistill {
				distillTargets = append(distillTargets, name)
			}
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
func applyPromptEdits(bundle *bundles.Bundle, set map[string]BundleSkillInput, remove []string, changes []string) (newChanges, distillTargets []string) {
	for name, in := range set {
		if bundle.Skills == nil {
			bundle.Skills = make(map[string]bundles.BundleSkill)
		}
		existing, hadExisting := bundle.Skills[name]
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
		bundle.Skills[name] = merged
		changes = append(changes, "set prompt: "+name)
	}
	for _, name := range remove {
		if _, ok := bundle.Skills[name]; ok {
			delete(bundle.Skills, name)
			changes = append(changes, "removed prompt: "+name)
		}
	}
	return changes, distillTargets
}

// onlyNewFragments returns the subset of in whose names are not already present
// in the bundle — the add-only filter so create-if-absent never overwrites.
func onlyNewFragments(b *bundles.Bundle, in map[string]BundleFragmentInput) map[string]BundleFragmentInput {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]BundleFragmentInput, len(in))
	for name, v := range in {
		if _, exists := b.Fragments[name]; !exists {
			out[name] = v
		}
	}
	return out
}

// onlyNewPrompts is the prompt counterpart of onlyNewFragments.
func onlyNewPrompts(b *bundles.Bundle, in map[string]BundleSkillInput) map[string]BundleSkillInput {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]BundleSkillInput, len(in))
	for name, v := range in {
		if _, exists := b.Skills[name]; !exists {
			out[name] = v
		}
	}
	return out
}

// onlyNewMCP is the MCP-server counterpart of onlyNewFragments.
func onlyNewMCP(b *bundles.Bundle, in map[string]BundleMCPInput) map[string]BundleMCPInput {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]BundleMCPInput, len(in))
	for name, v := range in {
		if _, exists := b.MCP[name]; !exists {
			out[name] = v
		}
	}
	return out
}

// applyMCPEdits merges set inputs into the bundle's MCP servers and applies
// removals, appending a change line per mutation. MCP servers carry no distilled
// content, so there are no distill targets to return.
func applyMCPEdits(bundle *bundles.Bundle, set map[string]BundleMCPInput, remove []string, changes []string) []string {
	for name, in := range set {
		if bundle.MCP == nil {
			bundle.MCP = make(map[string]bundles.BundleMCP)
		}
		existing := bundle.MCP[name]
		existing.Command = in.Command
		existing.Args = in.Args
		existing.Env = in.Env
		existing.Notes = in.Notes
		existing.Installation = in.Installation
		bundle.MCP[name] = existing
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
	if cfg == nil || len(cfg.AppPaths) == 0 {
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
}

// PushBundleResult reports what was (or would be) published.
type PushBundleResult struct {
	Status     string `json:"status"` // "preview" (dry-run), "pushed", "pr-created"
	Path       string `json:"path"`
	Remote     string `json:"remote"`      // Resolved registry name, or URL if no name match.
	TargetPath string `json:"target_path"` // Path inside the remote repo (e.g. ctxloom/bundles/foo.yaml).
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
}

// PushBundle publishes (or dry-runs) a local bundle file to req.Remote (a
// resolved registry name — see ResolveBundleRemote). Network is touched only
// for a non-dry-run.
func PushBundle(ctx context.Context, cfg *config.Config, req PushBundleRequest) (*PushBundleResult, error) {
	if err := validatePushRequest(cfg, req); err != nil {
		return nil, err
	}

	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	// Read + parse the bundle to validate it before any inference.
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	if _, err := bundles.ParseBundle(data); err != nil {
		return nil, fmt.Errorf("invalid bundle: %w", err)
	}

	registry, err := getRegistry(cfg)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	rem, err := registry.Get(req.Remote)
	if err != nil {
		return nil, fmt.Errorf("remote %q not found: %w", req.Remote, err)
	}

	// Bundle name in target repo = filename without .yaml extension.
	bundleName := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	targetPath := path.Join("ctxloom", "bundles", bundleName+".yaml")

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
	if cfg == nil || len(cfg.AppPaths) == 0 {
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

// runPush performs the actual (non-dry-run) publish and records the outcome on
// result.
func runPush(ctx context.Context, cfg *config.Config, registry *remote.Registry, remoteName, absPath string, req PushBundleRequest, result *PushBundleResult) (*PushBundleResult, error) {
	pm := req.PublishManager
	if pm == nil {
		pm = remote.NewPublishManager(registry, remote.LoadAuth(cfg.AppPaths[0]))
	}

	pubResult, err := pm.Publish(ctx, absPath, remoteName, remote.PublishOptions{
		CreatePR: req.CreatePR,
		Title:    result.Title,
		Message:  result.Message,
		ItemType: remote.ItemTypeBundle,
	})
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}

	result.CommitSHA = pubResult.SHA
	result.PRURL = pubResult.PRURL
	result.Status = "pushed"
	if req.CreatePR {
		result.Status = "pr-created"
	}
	return result, nil
}

// ResolveBundleRemote decides which configured remote a bundle file publishes
// to. An explicit override wins (after validating it exists); otherwise the
// remote is inferred from the bundle's location — a cache path or enclosing git
// origin, then the registry default, then a sole configured remote, else an
// ambiguous-remote error listing the candidates. Both the CLI and the VSCode
// companion call this before PushBundle, so neither re-implements the inference.
func ResolveBundleRemote(cfg *config.Config, bundlePath, override string) (string, error) {
	if cfg == nil || len(cfg.AppPaths) == 0 {
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
	// (1) Path under cache/bundles/<remote>/<rest>.yaml.
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

// remoteFromCachePath resolves the remote from a path under
// cache/bundles/<remote>/<rest>.yaml, requiring the first segment to be a known
// remote.
func remoteFromCachePath(cfg *config.Config, registry *remote.Registry, absPath string) (string, bool) {
	cacheRoot := paths.BundlesPath(cfg.AppPaths[0])
	rel, err := filepath.Rel(cacheRoot, absPath)
	if err != nil || isOutsideRel(rel) {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 2 && registry.Has(parts[0]) {
		return parts[0], true
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
	originURL, err := gitutil.GetOriginURL(absPath)
	if err != nil {
		return "", false
	}
	if name := matchRemoteByURL(registry, originURL); name != "" {
		return name, true
	}
	return "", false
}

// ambiguousRemoteError reports the candidate remotes (sorted) when no single
// remote can be inferred.
func ambiguousRemoteError(all []*remote.Remote) error {
	names := make([]string, 0, len(all))
	for _, r := range all {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return fmt.Errorf("ambiguous remote: configure a default or move the bundle under cache/bundles/<remote>/. Candidates: %s",
		strings.Join(names, ", "))
}

// isUnderCtxloom reports whether absPath lives under cfg's .ctxloom directory.
func isUnderCtxloom(cfg *config.Config, absPath string) bool {
	for _, app := range cfg.AppPaths {
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

// RequireSafeBundlePath is the exported guard for callers outside this package
// (the CLI `bundle edit` path, which mutates a loaded bundle in place and saves
// it directly rather than through UpdateBundle). It rejects a bundle path that
// escapes the given bundle dirs via a symlinked component, the same check
// CreateBundle/UpdateBundle/DeleteBundle apply internally.
func RequireSafeBundlePath(dirs []string, target string) error {
	return requireSafeBundlePath(dirs, target)
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
	normalized := remote.NormalizeURL(url)
	for _, r := range registry.List() {
		if remote.NormalizeURL(r.URL) == normalized {
			return r.Name
		}
	}
	return ""
}

// applyFragmentInputs writes fragment inputs into the bundle's Fragments map.
// Distillation is left to the caller (CreateBundle/UpdateBundle invoke the
// distiller after this).
func applyFragmentInputs(b *bundles.Bundle, in map[string]BundleFragmentInput) {
	if len(in) == 0 {
		return
	}
	if b.Fragments == nil {
		b.Fragments = make(map[string]bundles.BundleFragment, len(in))
	}
	for name, frag := range in {
		b.Fragments[name] = bundles.BundleFragment{
			Tags:         frag.Tags,
			Notes:        frag.Notes,
			Installation: frag.Installation,
			Content:      frag.Content,
			NoDistill:    frag.NoDistill,
		}
	}
}

func applyPromptInputs(b *bundles.Bundle, in map[string]BundleSkillInput) {
	if len(in) == 0 {
		return
	}
	if b.Skills == nil {
		b.Skills = make(map[string]bundles.BundleSkill, len(in))
	}
	for name, p := range in {
		b.Skills[name] = bundles.BundleSkill{
			Description:  p.Description,
			Tags:         p.Tags,
			Notes:        p.Notes,
			Installation: p.Installation,
			Content:      p.Content,
			NoDistill:    p.NoDistill,
		}
	}
}

func applyMCPInputs(b *bundles.Bundle, in map[string]BundleMCPInput) {
	if len(in) == 0 {
		return
	}
	if b.MCP == nil {
		b.MCP = make(map[string]bundles.BundleMCP, len(in))
	}
	for name, m := range in {
		b.MCP[name] = bundles.BundleMCP{
			Command:      m.Command,
			Args:         m.Args,
			Env:          m.Env,
			Notes:        m.Notes,
			Installation: m.Installation,
		}
	}
}

// namesNeedingFragmentDistill picks fragment names whose NoDistill is unset
// and that landed in the bundle. Deterministic order keeps tests stable.
func namesNeedingFragmentDistill(b *bundles.Bundle, in map[string]BundleFragmentInput) []string {
	if len(in) == 0 {
		return nil
	}
	names := make([]string, 0, len(in))
	for name, frag := range in {
		if frag.NoDistill {
			continue
		}
		if _, ok := b.Fragments[name]; !ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// namesNeedingPromptDistill is the prompt counterpart.
func namesNeedingPromptDistill(b *bundles.Bundle, in map[string]BundleSkillInput) []string {
	if len(in) == 0 {
		return nil
	}
	names := make([]string, 0, len(in))
	for name, p := range in {
		if p.NoDistill {
			continue
		}
		if _, ok := b.Skills[name]; !ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// distillFragments invokes the Distiller for each named fragment, populating
// Distilled / DistilledBy / ContentHash on success. Distill errors are warned
// but non-fatal: the bundle still saves with the raw content (fault-tolerance
// philosophy in CLAUDE.md). A nil Distiller is a no-op. The returned set names
// the items whose attempt FAILED — a re-distill failure leaves the old
// Distilled/DistilledBy intact, so post-state alone cannot reveal it.
func distillFragments(ctx context.Context, b *bundles.Bundle, names []string, d Distiller) collections.Set[string] {
	failed := collections.NewSet[string]()
	if d == nil || len(names) == 0 {
		return failed
	}
	for _, name := range names {
		frag := b.Fragments[name]
		res, err := d.Distill(ctx, DistillRequest{
			Kind:    DistillKindFragment,
			Name:    name,
			Content: frag.Content,
			Bundle:  b,
		})
		if err != nil {
			clidiag.Warn("ctxloom", "distill of fragment %q failed: %v", name, err)
			failed.Add(name)
			continue
		}
		frag.Distilled = res.Distilled
		frag.DistilledBy = res.ModelID
		frag.ContentHash = frag.ComputeContentHash()
		b.Fragments[name] = frag
	}
	return failed
}

// distillPrompts mirrors distillFragments for prompts.
func distillPrompts(ctx context.Context, b *bundles.Bundle, names []string, d Distiller) collections.Set[string] {
	failed := collections.NewSet[string]()
	if d == nil || len(names) == 0 {
		return failed
	}
	for _, name := range names {
		p := b.Skills[name]
		res, err := d.Distill(ctx, DistillRequest{
			Kind:    DistillKindSkill,
			Name:    name,
			Content: p.Content,
			Bundle:  b,
		})
		if err != nil {
			clidiag.Warn("ctxloom", "distill of prompt %q failed: %v", name, err)
			failed.Add(name)
			continue
		}
		p.Distilled = res.Distilled
		p.DistilledBy = res.ModelID
		p.ContentHash = p.ComputeContentHash()
		b.Skills[name] = p
	}
	return failed
}
