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
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitutil"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// DistillKind tags whether a Distill call is for a fragment or a prompt.
// The real LLM-backed distiller uses this to scope the sibling-context it
// builds (excluding the item being distilled).
type DistillKind string

const (
	DistillKindFragment DistillKind = "fragment"
	DistillKindPrompt   DistillKind = "prompt"
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
	Prompts     map[string]BundlePromptInput   `json:"prompts,omitempty"`
	MCPServers  map[string]BundleMCPInput      `json:"mcp_servers,omitempty"`

	// Distiller, when non-nil, is invoked for each new fragment whose
	// NoDistill flag is unset. Distill failures are warned to stderr but
	// do not fail the create (fault-tolerance principle).
	Distiller Distiller `json:"-"`
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

// BundlePromptInput describes a prompt to add or update via operations.
type BundlePromptInput struct {
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
	applyPromptInputs(bundle, req.Prompts)
	applyMCPInputs(bundle, req.MCPServers)

	distillFragments(ctx, bundle, namesNeedingFragmentDistill(bundle, req.Fragments), req.Distiller)
	distillPrompts(ctx, bundle, namesNeedingPromptDistill(bundle, req.Prompts), req.Distiller)

	if err := bundle.Save(); err != nil {
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

	SetFragments    map[string]BundleFragmentInput `json:"set_fragments,omitempty"`
	RemoveFragments []string                       `json:"remove_fragments,omitempty"`

	SetPrompts    map[string]BundlePromptInput `json:"set_prompts,omitempty"`
	RemovePrompts []string                     `json:"remove_prompts,omitempty"`

	SetMCPServers    map[string]BundleMCPInput `json:"set_mcp_servers,omitempty"`
	RemoveMCPServers []string                  `json:"remove_mcp_servers,omitempty"`

	// Distill, when *false, skips distillation entirely (matching MCP-tool
	// "distill: false" opt-out). Default (nil or *true) follows per-fragment
	// NoDistill flags.
	Distill   *bool     `json:"distill,omitempty"`
	Distiller Distiller `json:"-"`
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
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if err := bundles.ValidateBundleName(req.Name); err != nil {
		return nil, err
	}
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	// Loader resolves the bundle name to whatever path it finds on disk; if
	// any component (or the file itself) is a symlink, Save would follow it
	// out of the bundles tree. Reject before mutating.
	if err := requireSafeBundlePath(cfg.GetBundleDirs(), bundle.Path); err != nil {
		return nil, err
	}

	var changes []string

	if req.SetDescription != nil {
		bundle.Description = *req.SetDescription
		changes = append(changes, "updated description")
	}
	if req.SetVersion != nil {
		bundle.Version = *req.SetVersion
		changes = append(changes, "updated version")
	}

	for _, tag := range req.AddTags {
		if !containsString(bundle.Tags, tag) {
			bundle.Tags = append(bundle.Tags, tag)
			changes = append(changes, "added tag: "+tag)
		}
	}
	for _, tag := range req.RemoveTags {
		if containsString(bundle.Tags, tag) {
			bundle.Tags = removeString(bundle.Tags, tag)
			changes = append(changes, "removed tag: "+tag)
		}
	}

	// Track which fragments/prompts need (re)distillation: new items, or
	// existing items whose content actually changed. Tag/notes-only edits
	// must not wipe existing Distilled/DistilledBy/ContentHash and must not
	// trigger a redistill (which would burn tokens for no semantic change).
	var fragmentDistillTargets, promptDistillTargets []string
	for name, in := range req.SetFragments {
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
				fragmentDistillTargets = append(fragmentDistillTargets, name)
			}
		}
		bundle.Fragments[name] = merged
		changes = append(changes, "set fragment: "+name)
	}
	for _, name := range req.RemoveFragments {
		if _, ok := bundle.Fragments[name]; ok {
			delete(bundle.Fragments, name)
			changes = append(changes, "removed fragment: "+name)
		}
	}

	for name, in := range req.SetPrompts {
		if bundle.Prompts == nil {
			bundle.Prompts = make(map[string]bundles.BundlePrompt)
		}
		existing, hadExisting := bundle.Prompts[name]
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
				promptDistillTargets = append(promptDistillTargets, name)
			}
		}
		bundle.Prompts[name] = merged
		changes = append(changes, "set prompt: "+name)
	}
	for _, name := range req.RemovePrompts {
		if _, ok := bundle.Prompts[name]; ok {
			delete(bundle.Prompts, name)
			changes = append(changes, "removed prompt: "+name)
		}
	}

	for name, in := range req.SetMCPServers {
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
	for _, name := range req.RemoveMCPServers {
		if _, ok := bundle.MCP[name]; ok {
			delete(bundle.MCP, name)
			changes = append(changes, "removed mcp: "+name)
		}
	}

	if len(changes) == 0 {
		return &UpdateBundleResult{Status: "no_changes", Name: req.Name, Path: bundle.Path}, nil
	}

	wholesaleSkip := req.Distill != nil && !*req.Distill
	if !wholesaleSkip {
		sort.Strings(fragmentDistillTargets)
		sort.Strings(promptDistillTargets)
		distillFragments(ctx, bundle, fragmentDistillTargets, req.Distiller)
		distillPrompts(ctx, bundle, promptDistillTargets, req.Distiller)
	}

	if err := bundle.Save(); err != nil {
		return nil, fmt.Errorf("failed to save bundle: %w", err)
	}

	return &UpdateBundleResult{
		Status:  "updated",
		Name:    req.Name,
		Changes: changes,
		Path:    bundle.Path,
	}, nil
}

// DeleteBundleRequest is the input for DeleteBundle.
type DeleteBundleRequest struct {
	Name string `json:"name"`
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

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	// os.Remove on a symlink removes the link (not its target), so delete
	// is intrinsically safer than write — but a symlinked parent could
	// still steer the loader at a file outside the bundles tree, so apply
	// the same check Update uses.
	if err := requireSafeBundlePath(cfg.GetBundleDirs(), bundle.Path); err != nil {
		return nil, err
	}

	if err := os.Remove(bundle.Path); err != nil {
		return nil, fmt.Errorf("failed to remove bundle file: %w", err)
	}

	return &DeleteBundleResult{Status: "deleted", Name: req.Name, Path: bundle.Path}, nil
}

// PushBundleRequest is the input for PushBundle.
//
// The user told us "file path *only*": the path identifies which bundle to
// push and (via its directory layout / enclosing git tree) implies the
// remote target. Callers don't need to specify the remote name — but they
// can override commit metadata.
type PushBundleRequest struct {
	Path string `json:"path"`
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

// PushBundle publishes (or dry-runs) a local bundle file to a remote repo.
// Remote inference order:
//  1. <appPath>/cache/bundles/<remote>/<rest>.yaml — first segment is the remote
//  2. <appPath>/cache/bundles/<rest>.yaml — registry default
//  3. exactly one configured remote — use it
//  4. error with candidate list
//
// Network is touched only for non-dry-run; this commit covers dry-run.
func PushBundle(ctx context.Context, cfg *config.Config, req PushBundleRequest) (*PushBundleResult, error) {
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
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

	remoteName, err := resolveRemoteForPath(cfg, registry, absPath)
	if err != nil {
		return nil, err
	}

	// Bundle name in target repo = filename without .yaml extension.
	bundleName := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))

	rem, err := registry.Get(remoteName)
	if err != nil {
		return nil, fmt.Errorf("remote %q not found: %w", remoteName, err)
	}
	targetPath := fmt.Sprintf("ctxloom/bundles/%s.yaml", bundleName)

	// Resolve title/body the same way publish.go does, so the result accurately
	// reflects what the PR will look like (title may be lifted from message).
	title := strings.TrimSpace(req.Title)
	message := strings.TrimSpace(req.Message)
	if title == "" && message != "" {
		if idx := strings.IndexByte(message, '\n'); idx >= 0 {
			title = strings.TrimSpace(message[:idx])
			message = strings.TrimSpace(message[idx+1:])
		} else {
			title = message
			message = ""
		}
	}
	if title == "" {
		title = fmt.Sprintf("Update bundle: %s", bundleName)
	}

	result := &PushBundleResult{
		Path:       absPath,
		Remote:     remoteName,
		TargetPath: targetPath,
		Title:      title,
		Message:    message,
		CreatePR:   req.CreatePR,
		SizeBytes:  len(data),
	}

	if req.DryRun {
		action := "direct push"
		if req.CreatePR {
			action = "pull request"
		}
		result.Status = "preview"
		result.Preview = fmt.Sprintf("Would publish %s (%d bytes) to %s as %s via %s; title: %q",
			bundleName, len(data), rem.URL, targetPath, action, title)
		return result, nil
	}

	pm := req.PublishManager
	if pm == nil {
		pm = remote.NewPublishManager(registry, remote.LoadAuth(cfg.AppPaths[0]))
	}

	pubResult, err := pm.Publish(ctx, absPath, remoteName, remote.PublishOptions{
		CreatePR: req.CreatePR,
		Title:    title,
		Message:  message,
		ItemType: remote.ItemTypeBundle,
	})
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}

	result.CommitSHA = pubResult.SHA
	result.PRURL = pubResult.PRURL
	if req.CreatePR {
		result.Status = "pr-created"
	} else {
		result.Status = "pushed"
	}
	return result, nil
}

// resolveRemoteForPath implements the inference order documented on
// PushBundle. Returns the remote name to publish to.
func resolveRemoteForPath(cfg *config.Config, registry *remote.Registry, absPath string) (string, error) {
	// (1) Path under cache/bundles/<remote>/<rest>.yaml.
	cacheRoot := paths.BundlesPath(cfg.AppPaths[0])
	if rel, err := filepath.Rel(cacheRoot, absPath); err == nil && !isOutsideRel(rel) {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) >= 2 {
			candidate := parts[0]
			if registry.Has(candidate) {
				return candidate, nil
			}
		}
	}

	// (2) Path is OUTSIDE .ctxloom and lives in a git working tree — match
	// the tree's origin URL against the registry. Only triggers for paths
	// outside the .ctxloom directory: a bundle inside .ctxloom/cache might
	// be in the project's git tree, but that doesn't tell us where to push
	// the bundle (the project repo is rarely a bundles repo).
	if !isUnderCtxloom(cfg, absPath) {
		if originURL, err := gitutil.GetOriginURL(absPath); err == nil {
			if name := matchRemoteByURL(registry, originURL); name != "" {
				return name, nil
			}
		}
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
		names := make([]string, 0, len(all))
		for _, r := range all {
			names = append(names, r.Name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("ambiguous remote: configure a default or move the bundle under cache/bundles/<remote>/. Candidates: %s",
			strings.Join(names, ", "))
	}

	return "", fmt.Errorf("no remote configured: add one with `ctxloom remote add`")
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
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absDir, absTarget)
		if err != nil || isOutsideRel(rel) {
			continue
		}
		if rel == "." {
			return nil
		}
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
	return fmt.Errorf("bundle path %s not under any configured bundles directory", target)
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

func containsString(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}

func removeString(s []string, target string) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v != target {
			out = append(out, v)
		}
	}
	return out
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

func applyPromptInputs(b *bundles.Bundle, in map[string]BundlePromptInput) {
	if len(in) == 0 {
		return
	}
	if b.Prompts == nil {
		b.Prompts = make(map[string]bundles.BundlePrompt, len(in))
	}
	for name, p := range in {
		b.Prompts[name] = bundles.BundlePrompt{
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
func namesNeedingPromptDistill(b *bundles.Bundle, in map[string]BundlePromptInput) []string {
	if len(in) == 0 {
		return nil
	}
	names := make([]string, 0, len(in))
	for name, p := range in {
		if p.NoDistill {
			continue
		}
		if _, ok := b.Prompts[name]; !ok {
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
// philosophy in CLAUDE.md). A nil Distiller is a no-op.
func distillFragments(ctx context.Context, b *bundles.Bundle, names []string, d Distiller) {
	if d == nil || len(names) == 0 {
		return
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
			fmt.Fprintf(os.Stderr, "ctxloom: warning: distill of fragment %q failed: %v\n", name, err)
			continue
		}
		frag.Distilled = res.Distilled
		frag.DistilledBy = res.ModelID
		frag.ContentHash = frag.ComputeContentHash()
		b.Fragments[name] = frag
	}
}

// distillPrompts mirrors distillFragments for prompts.
func distillPrompts(ctx context.Context, b *bundles.Bundle, names []string, d Distiller) {
	if d == nil || len(names) == 0 {
		return
	}
	for _, name := range names {
		p := b.Prompts[name]
		res, err := d.Distill(ctx, DistillRequest{
			Kind:    DistillKindPrompt,
			Name:    name,
			Content: p.Content,
			Bundle:  b,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: distill of prompt %q failed: %v\n", name, err)
			continue
		}
		p.Distilled = res.Distilled
		p.DistilledBy = res.ModelID
		p.ContentHash = p.ComputeContentHash()
		b.Prompts[name] = p
	}
}
