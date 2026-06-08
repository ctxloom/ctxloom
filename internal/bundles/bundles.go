// Package bundles provides types and utilities for ctxloom bundles.
// Bundles are the primary content unit that group related fragments,
// prompts, and MCP server configurations with a single version.
package bundles

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/shared/collections"
)

// Bundle represents a versioned collection of related content.
// All items within a bundle share the same version.
// Each fragment and prompt is distilled individually with bundle context.
type Bundle struct {
	// Metadata
	Version      string   `yaml:"version"`
	Tags         []string `yaml:"tags,omitempty"`
	Author       string   `yaml:"author,omitempty"`
	Description  string   `yaml:"description,omitempty"`
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup instructions, shown on install

	// Content maps (keyed by name)
	Fragments map[string]BundleFragment `yaml:"fragments,omitempty"`
	Prompts   map[string]BundlePrompt   `yaml:"prompts,omitempty"`
	MCP       map[string]BundleMCP      `yaml:"mcp,omitempty"` // MCP servers

	// Hooks shipped with this bundle (e.g. PostFileEdit plan-stamping).
	// Hooks land in backend settings via ApplyHooks → ResolveBundleHooks.
	// Bundle-shipped hooks are subject to the same review gate as bundle
	// fragments/prompts/MCP: a remote-sourced bundle whose SHA changed must
	// be acknowledged before its hooks fire (see docs/bundle-review-plan.md).
	Hooks BundleHooks `yaml:"hooks,omitempty"`

	// Internal fields (not serialized)
	Name string `yaml:"-"` // Bundle name (from path)
	Path string `yaml:"-"` // File path for saving
}

// BundleHook is the bundle-authoring shape of a hook: the wire.Hook fields a
// bundle may declare, minus the SCM marker (bundle hooks are stamped at the
// operations boundary, not hand-authored). The conversion to wire.Hook lives in
// config.ResolveBundleHooks.
type BundleHook struct {
	Matcher string `yaml:"matcher,omitempty"`
	Command string `yaml:"command,omitempty"`
	Type    string `yaml:"type,omitempty"`
	Prompt  string `yaml:"prompt,omitempty"`
	Timeout int    `yaml:"timeout,omitempty"`
	Async   bool   `yaml:"async,omitempty"`
}

// BundleHooks mirrors wire.UnifiedHooks. Same lifecycle events; backend-
// specific hooks are deliberately not supported in bundles (would couple
// authoring to a particular backend's tool naming).
type BundleHooks struct {
	PreTool      []BundleHook `yaml:"pre_tool,omitempty"`
	PostTool     []BundleHook `yaml:"post_tool,omitempty"`
	SessionStart []BundleHook `yaml:"session_start,omitempty"`
	SessionEnd   []BundleHook `yaml:"session_end,omitempty"`
	PreShell     []BundleHook `yaml:"pre_shell,omitempty"`
	PostFileEdit []BundleHook `yaml:"post_file_edit,omitempty"`
}

// HasAny reports whether the bundle ships any hooks. Used by the loader to
// skip the merge cost for hookless bundles.
func (h BundleHooks) HasAny() bool {
	return len(h.PreTool)+len(h.PostTool)+len(h.SessionStart)+len(h.SessionEnd)+len(h.PreShell)+len(h.PostFileEdit) > 0
}

// BundleMCP defines an MCP server within a bundle.
type BundleMCP struct {
	Command      string            `yaml:"command"`
	Args         []string          `yaml:"args,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	Notes        string            `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string            `yaml:"installation,omitempty"` // Setup/installation instructions, sent to AI
}

// BundleFragment defines a fragment within a bundle.
type BundleFragment struct {
	Tags         []string `yaml:"tags,omitempty"`         // Additional tags (merged with bundle tags)
	Notes        string   `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string   `yaml:"installation,omitempty"` // Setup/installation instructions, sent to AI
	Content      string   `yaml:"content"`
	ContentHash  string   `yaml:"content_hash,omitempty"`
	Distilled    string   `yaml:"distilled,omitempty"`
	DistilledBy  string   `yaml:"distilled_by,omitempty"`
	NoDistill    bool     `yaml:"no_distill,omitempty"`
}

// BundlePrompt defines a prompt within a bundle.
type BundlePrompt struct {
	Description  string     `yaml:"description,omitempty"`
	Tags         []string   `yaml:"tags,omitempty"`
	Notes        string     `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string     `yaml:"installation,omitempty"` // Setup/installation instructions, sent to AI
	Content      string     `yaml:"content"`
	ContentHash  string     `yaml:"content_hash,omitempty"`
	Distilled    string     `yaml:"distilled,omitempty"`
	DistilledBy  string     `yaml:"distilled_by,omitempty"`
	NoDistill    bool       `yaml:"no_distill,omitempty"`
	LLM          LLMExports `yaml:"llm,omitempty"` // Per-LLM export settings (e.g. claude-code slash-command config)
}

// ComputeContentHash computes SHA256 hash of the content.
func (f *BundleFragment) ComputeContentHash() string {
	h := sha256.Sum256([]byte(f.Content))
	return "sha256:" + hex.EncodeToString(h[:])
}

// NeedsDistill returns true if this fragment needs distillation.
func (f *BundleFragment) NeedsDistill() bool {
	if f.NoDistill {
		return false
	}
	if f.Distilled == "" {
		return true
	}
	if f.ContentHash == "" {
		return true
	}
	return f.ContentHash != f.ComputeContentHash()
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (f *BundleFragment) EffectiveContent(preferDistilled bool) string {
	if preferDistilled && f.Distilled != "" && !f.NoDistill {
		return f.Distilled
	}
	return f.Content
}

// ComputeContentHash computes SHA256 hash of the content.
func (p *BundlePrompt) ComputeContentHash() string {
	h := sha256.Sum256([]byte(p.Content))
	return "sha256:" + hex.EncodeToString(h[:])
}

// NeedsDistill returns true if this prompt needs distillation.
func (p *BundlePrompt) NeedsDistill() bool {
	if p.NoDistill {
		return false
	}
	if p.Distilled == "" {
		return true
	}
	if p.ContentHash == "" {
		return true
	}
	return p.ContentHash != p.ComputeContentHash()
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (p *BundlePrompt) EffectiveContent(preferDistilled bool) string {
	if preferDistilled && p.Distilled != "" && !p.NoDistill {
		return p.Distilled
	}
	return p.Content
}

// HasMCP returns true if bundle includes any MCP servers.
func (b *Bundle) HasMCP() bool {
	return len(b.MCP) > 0
}

// MCPCount returns the number of MCP servers in the bundle.
func (b *Bundle) MCPCount() int {
	return len(b.MCP)
}

// MCPNames returns sorted MCP server names.
func (b *Bundle) MCPNames() []string {
	names := make([]string, 0, len(b.MCP))
	for name := range b.MCP {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FragmentCount returns the number of fragments in the bundle.
func (b *Bundle) FragmentCount() int {
	return len(b.Fragments)
}

// PromptCount returns the number of prompts in the bundle.
func (b *Bundle) PromptCount() int {
	return len(b.Prompts)
}

// FragmentNames returns sorted fragment names.
func (b *Bundle) FragmentNames() []string {
	names := make([]string, 0, len(b.Fragments))
	for name := range b.Fragments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PromptNames returns sorted prompt names.
func (b *Bundle) PromptNames() []string {
	names := make([]string, 0, len(b.Prompts))
	for name := range b.Prompts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AllTags returns all unique tags from bundle and its contents.
func (b *Bundle) AllTags() []string {
	tagSet := collections.NewSet[string]()
	tagSet.AddAll(b.Tags...)
	for _, f := range b.Fragments {
		tagSet.AddAll(f.Tags...)
	}
	for _, p := range b.Prompts {
		tagSet.AddAll(p.Tags...)
	}

	tags := tagSet.Items()
	sort.Strings(tags)
	return tags
}

// AssembledContent combines all fragment content with separators.
func (b *Bundle) AssembledContent(preferDistilled bool) string {
	var parts []string

	for _, name := range b.FragmentNames() {
		frag := b.Fragments[name]
		content := frag.EffectiveContent(preferDistilled)
		parts = append(parts, strings.TrimSpace(content))
	}

	return strings.Join(parts, "\n\n---\n\n")
}

// ParseBundle parses raw YAML into a Bundle.
func ParseBundle(data []byte) (*Bundle, error) {
	var bundle Bundle
	if err := yaml.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("invalid bundle YAML: %w", err)
	}

	// Initialize maps if nil
	if bundle.Fragments == nil {
		bundle.Fragments = make(map[string]BundleFragment)
	}
	if bundle.Prompts == nil {
		bundle.Prompts = make(map[string]BundlePrompt)
	}
	if bundle.MCP == nil {
		bundle.MCP = make(map[string]BundleMCP)
	}

	return &bundle, nil
}

// ValidateBundleName rejects bundle names that would escape the bundles
// directory when joined to a base path. This is the single chokepoint for
// path-traversal defense — every caller that builds a filesystem path from a
// user/AI-supplied bundle name MUST run names through this first.
//
// Rejected:
//   - Empty names.
//   - Names containing null bytes (would truncate the path on some C APIs).
//   - Names that resolve to a path starting with ".." after filepath.Clean
//     (e.g. "../etc/passwd", "foo/../../escape", "../"+anything).
//   - Absolute paths ("/etc/passwd", "C:\\Windows\\…").
//
// Accepted:
//   - Simple names: "my-bundle".
//   - Slash-separated names: "personal/foo", "alice/go-tools". These are
//     used to place bundles under a remote subdirectory; filepath.Join keeps
//     them under the bundles root.
//   - Names that contain ".." in the middle without escaping after Clean
//     (e.g. "foo/../bar" cleans to "bar" — safe but probably an AI bug).
//
// Threat model: MCP tools accept names from AI clients, so an untrusted name
// could request ".." segments. Without this check, filepath.Join silently
// resolves "../../../tmp/evil" into a path outside the bundles root and
// Bundle.Save would write to it. Empirically verified during the bundle-MCP
// review on feat/bundle-mcp-tools.
//
// Not covered by this function: symlinks already on disk inside the bundles
// tree. ValidateBundleName only inspects the string; it doesn't lstat path
// components. The operations layer pairs this with a requireSafeBundlePath
// walk that rejects symlinked directory components and symlinked bundle
// files — both defenses are needed.
func ValidateBundleName(name string) error {
	if name == "" {
		return fmt.Errorf("empty bundle name")
	}

	// Check for null bytes first (before any path operations)
	if strings.ContainsAny(name, "\x00") {
		return fmt.Errorf("invalid bundle name: null bytes not allowed")
	}

	// Normalize path first
	cleaned := filepath.Clean(name)

	// Check for traversal after cleaning (catches "....", "foo/../bar", etc.)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return fmt.Errorf("invalid bundle name: path traversal not allowed")
	}

	return nil
}

// extractBundleName extracts bundle name from file path.
func extractBundleName(path string) string {
	base := filepath.Base(path)

	// If it's bundle.yaml, use parent directory name
	if base == "bundle.yaml" {
		return filepath.Base(filepath.Dir(path))
	}

	// Otherwise use filename without extension
	return strings.TrimSuffix(base, filepath.Ext(base))
}
