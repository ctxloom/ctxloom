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

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// Distiller compresses fragment/prompt content via an LLM. The operations
// layer is provider-agnostic: callers (MCP server, future CLI refactor) wire
// up a real LLM-backed implementation; tests inject a mock. A nil Distiller
// means "skip distillation"; bundles still save with raw content.
type Distiller interface {
	// Distill returns the distilled content and the model identifier that
	// produced it. The name is the fragment/prompt key, useful for logging
	// and for any sibling-context the implementation needs to assemble.
	Distill(ctx context.Context, name, content string) (distilled, modelID string, err error)
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
// in UpdateBundleRequest (see L3/L4 tests).
type BundleFragmentInput struct {
	Content     string   `json:"content"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	NoDistill   bool     `json:"no_distill,omitempty"`
}

// BundlePromptInput describes a prompt to add or update via operations.
type BundlePromptInput struct {
	Content     string   `json:"content"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	NoDistill   bool     `json:"no_distill,omitempty"`
}

// BundleMCPInput describes an MCP server entry to add or update via operations.
type BundleMCPInput struct {
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Description string            `json:"description,omitempty"`
}

// CreateBundleResult is what CreateBundle returns on success.
type CreateBundleResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// CreateBundle writes a new bundle YAML to .ctxloom/cache/bundles/<name>.yaml.
func CreateBundle(ctx context.Context, cfg *config.Config, req CreateBundleRequest) (*CreateBundleResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	dir := paths.BundlesPath(cfg.AppPaths[0])
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}
	path := filepath.Join(dir, req.Name+".yaml")
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

	distillFragments(ctx, bundle, namesNeedingDistill(bundle, req.Fragments), req.Distiller)

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
func UpdateBundle(ctx context.Context, cfg *config.Config, req UpdateBundleRequest) (*UpdateBundleResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
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

	// Track which fragments are new/updated so distill targets exactly those.
	var distillTargets []string
	for name, in := range req.SetFragments {
		if bundle.Fragments == nil {
			bundle.Fragments = make(map[string]bundles.BundleFragment)
		}
		bundle.Fragments[name] = bundles.BundleFragment{
			Tags:      in.Tags,
			Content:   in.Content,
			NoDistill: in.NoDistill,
		}
		if !in.NoDistill {
			distillTargets = append(distillTargets, name)
		}
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
		bundle.Prompts[name] = bundles.BundlePrompt{
			Description: in.Description,
			Tags:        in.Tags,
			Content:     in.Content,
			NoDistill:   in.NoDistill,
		}
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
		bundle.MCP[name] = bundles.BundleMCP{
			Command: in.Command,
			Args:    in.Args,
			Env:     in.Env,
		}
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
		sort.Strings(distillTargets)
		distillFragments(ctx, bundle, distillTargets, req.Distiller)
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
func DeleteBundle(_ context.Context, cfg *config.Config, req DeleteBundleRequest) (*DeleteBundleResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)
	bundle, err := loader.Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}

	if err := os.Remove(bundle.Path); err != nil {
		return nil, fmt.Errorf("failed to remove bundle file: %w", err)
	}

	return &DeleteBundleResult{Status: "deleted", Name: req.Name, Path: bundle.Path}, nil
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
			Tags:      frag.Tags,
			Content:   frag.Content,
			NoDistill: frag.NoDistill,
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
			Description: p.Description,
			Tags:        p.Tags,
			Content:     p.Content,
			NoDistill:   p.NoDistill,
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
			Command: m.Command,
			Args:    m.Args,
			Env:     m.Env,
		}
	}
}

// namesNeedingDistill picks fragment names whose NoDistill is unset. We
// pass through the input map because b.Fragments[name].NoDistill round-trips
// from the input but we want a deterministic order for tests; sorting here
// keeps test mocks predictable.
func namesNeedingDistill(b *bundles.Bundle, in map[string]BundleFragmentInput) []string {
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
		distilled, modelID, err := d.Distill(ctx, name, frag.Content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: distill of fragment %q failed: %v\n", name, err)
			continue
		}
		frag.Distilled = distilled
		frag.DistilledBy = modelID
		frag.ContentHash = frag.ComputeContentHash()
		b.Fragments[name] = frag
	}
}
