// Package bundles provides types and utilities for ctxloom bundles.
// Bundles are the primary content unit that group related fragments,
// prompts, MCP server configurations, and profiles with a single version.
package bundles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
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
	Skills    map[string]BundleSkill    `yaml:"skills,omitempty"`
	MCP       map[string]BundleMCP      `yaml:"mcp,omitempty"` // MCP servers

	// Profiles shipped with this bundle, keyed by name. A profile is an
	// ungated, COMPOUND item — it composes leaves (fragments/skills/mcp/hooks/
	// llm/parents/variables) into a runnable context unit — so a bundle that
	// ships fragments can also ship the profiles that compose them, as one unit.
	// Addressed by "<bundle>#profiles/<name>" (remote.ProfileSelector) and seeded
	// into the shared profile loader so a bundle profile resolves/runs exactly
	// like a top-level or local profile (config bundle-profile seed). The profile
	// DEFINITION is never trust-gated (no trust.ItemKind for profiles, never
	// baselined); its constituent fragments/skills still gate at content
	// assembly and any mcp/hooks it pulls in still gate at the exec choke.
	Profiles map[string]BundleProfile `yaml:"profiles,omitempty"`

	// Hooks shipped with this bundle (e.g. PostFileEdit plan-stamping).
	// Hooks land in backend settings via ApplyHooks → ResolveBundleHooks.
	// Bundle-shipped hooks are subject to the same review gate as bundle
	// fragments/prompts/MCP: a remote-sourced bundle whose SHA changed must
	// be acknowledged before its hooks fire (see docs/bundle-review-plan.md).
	Hooks BundleHooks `yaml:"hooks,omitempty"`

	// Internal fields (not serialized)
	Name string `yaml:"-"` // Bundle name (from path)
	Path string `yaml:"-"` // File path for saving
	// sourceRef is the bundle's canonical ref when it was seeded from a remote
	// (cloned) source — the lockfile key shape, e.g. "https://…@bundles/x". It is
	// empty for a project (fs) bundle. Set at seed time (WithSeededBundles).
	sourceRef string `yaml:"-"`
}

// contentSourceRef returns the bundle's honest source ref for content trust
// gating: the canonical ref of a seeded (cloned) bundle, or the local bundle.Name
// for a project (fs) bundle. Locality flows from this into the trust cascade, so
// a clone's TEXT gates like its executables while a project bundle's text
// auto-trusts. "Text to an LLM is executable."
func (b *Bundle) contentSourceRef() string {
	if b.sourceRef != "" {
		return b.sourceRef
	}
	return b.Name
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
	// PreToolFallback (session_start only): the hook is idempotent and may
	// fire on PreToolUse instead on agents without a session-start event.
	// See wire.Hook.PreToolFallback.
	PreToolFallback bool `yaml:"pre_tool_fallback,omitempty"`
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

// Hook event names. They double as the stable event component of a bundle
// hook's trust identity ("<bundle>#hooks/<event>/<index>") and as the canonical
// iteration order below, and match the BundleHooks YAML field tags.
const (
	HookEventPreTool      = "pre_tool"
	HookEventPostTool     = "post_tool"
	HookEventSessionStart = "session_start"
	HookEventSessionEnd   = "session_end"
	HookEventPreShell     = "pre_shell"
	HookEventPostFileEdit = "post_file_edit"
)

// hookEventOrder is the canonical event order for hook identity + enumeration.
// Entries() and the trust gate both walk it so a baselined hook's ref matches
// the one the gate evaluates.
var hookEventOrder = []string{
	HookEventPreTool, HookEventPostTool, HookEventSessionStart,
	HookEventSessionEnd, HookEventPreShell, HookEventPostFileEdit,
}

// eventHooks returns the hook slice for an event (nil for an unknown event).
func (h BundleHooks) eventHooks(event string) []BundleHook {
	switch event {
	case HookEventPreTool:
		return h.PreTool
	case HookEventPostTool:
		return h.PostTool
	case HookEventSessionStart:
		return h.SessionStart
	case HookEventSessionEnd:
		return h.SessionEnd
	case HookEventPreShell:
		return h.PreShell
	case HookEventPostFileEdit:
		return h.PostFileEdit
	}
	return nil
}

// HookEntry is one bundle hook paired with its stable trust identity: the event
// it fires on and its index within that event's list. Bundle hooks are an
// ordered list with no author-given name, so (event, index) is the addressable
// identity the per-item trust gate keys on (trust rework, TR5).
type HookEntry struct {
	Event string
	Index int
	Hook  BundleHook
}

// ID returns the stable per-hook identity "<event>/<index>", the <id> in the
// trust ref "<bundle>#hooks/<id>". The index is the hook's authored position in
// its event list.
func (e HookEntry) ID() string {
	return e.Event + "/" + strconv.Itoa(e.Index)
}

// Entries returns every bundle hook with its identity, in canonical event order
// then authored index. The trust gate (config.extractHooksFromBundle) and the
// migration baseline both enumerate hooks through this scheme so the refs agree.
func (h BundleHooks) Entries() []HookEntry {
	var out []HookEntry
	for _, event := range hookEventOrder {
		for i, hook := range h.eventHooks(event) {
			out = append(out, HookEntry{Event: event, Index: i, Hook: hook})
		}
	}
	return out
}

// EntryByID resolves a hook identity ("<event>/<index>") back to its entry. It
// reports ok=false for a malformed id or an out-of-range index — fail-closed: an
// unresolvable hook hashes to nothing and gates.
func (h BundleHooks) EntryByID(id string) (HookEntry, bool) {
	event, idxStr, found := strings.Cut(id, "/")
	if !found {
		return HookEntry{}, false
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 {
		return HookEntry{}, false
	}
	hooks := h.eventHooks(event)
	if idx >= len(hooks) {
		return HookEntry{}, false
	}
	return HookEntry{Event: event, Index: idx, Hook: hooks[idx]}, true
}

// BundleMCP defines an MCP server within a bundle.
type BundleMCP struct {
	Command      string            `yaml:"command"`
	Args         []string          `yaml:"args,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	Notes        string            `yaml:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string            `yaml:"installation,omitempty"` // Setup/installation instructions, sent to AI
	ContentHash  string            `yaml:"content_hash,omitempty"` // Hash of the executable surface (Command+Args+Env+Installation)
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

// BundleSkill defines a prompt within a bundle.
type BundleSkill struct {
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

// BundleProfile is the shape of a profile shipped inside a bundle. It is the
// SAME type as a directory/top-level profile (profiles.Profile), so a bundle
// profile and an inline/config or remote profile resolve through one code path:
// the bundle-profile seed parses these straight into the shared profile loader.
// Reusing the type (rather than mirroring its fields) keeps the two from
// drifting and means a profile composes the same leaves wherever it is authored.
// The bundle YAML map key supplies the profile's Name (the struct's Name/Path
// are yaml:"-"); the seed sets Name to the "<bundle>#profiles/<name>" identity.
type BundleProfile = profiles.Profile

// ContentForm identifies which materialization of an item's content was hashed
// or served: the raw authored bytes, or the distilled rewrite. Trust grants
// (trust rework, TR0+) bind {effective-content-hash, form} together so a grant
// blessing the raw form can never validate a distilled exposure, and vice-versa.
type ContentForm string

const (
	FormRaw       ContentForm = "raw"
	FormDistilled ContentForm = "distilled"
)

// hashContent is the single sha256 helper every content-hash computation in this
// package routes through. It returns the canonical "sha256:<hex>" digest.
func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// resolveEffective is the one shared compute primitive for the distillable item
// shape. Fragments and prompts carry the same Content/Distilled/NoDistill fields,
// so this picks the bytes to expose AND reports their form from the same
// predicate — guaranteeing a hash taken over the result covers exactly the served
// bytes, with no raw fallback once distilled is chosen.
func resolveEffective(preferDistilled bool, content, distilled string, noDistill bool) (string, ContentForm) {
	if preferDistilled && distilled != "" && !noDistill {
		return distilled, FormDistilled
	}
	return content, FormRaw
}

// staleDistill is the one shared compare primitive: it reports whether a
// distillable item's distilled form is stale relative to its raw content (the
// re-distillation check). It compares the RECORDED hash against a freshly
// computed one — independent of trust, which never reads the author-supplied
// recorded field. Sharing it keeps fragments and prompts from drifting
// (review finding ctxloom-code-09-003).
func staleDistill(noDistill bool, distilled, recordedHash, content string) bool {
	if noDistill {
		return false
	}
	if distilled == "" {
		return true
	}
	if recordedHash == "" {
		return true
	}
	return recordedHash != hashContent([]byte(content))
}

// ComputeContentHash computes the SHA256 hash of the raw authored content. This
// feeds the recorded content_hash that drives re-distillation (NeedsDistill); the
// trust gate uses EffectiveContentHash instead.
func (f *BundleFragment) ComputeContentHash() string {
	return hashContent([]byte(f.Content))
}

// NeedsDistill returns true if this fragment needs distillation.
func (f *BundleFragment) NeedsDistill() bool {
	return staleDistill(f.NoDistill, f.Distilled, f.ContentHash, f.Content)
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (f *BundleFragment) EffectiveContent(preferDistilled bool) string {
	content, _ := resolveEffective(preferDistilled, f.Content, f.Distilled, f.NoDistill)
	return content
}

// EffectiveContentHash hashes EXACTLY the bytes EffectiveContent(preferDistilled)
// returns, and reports their form. This is the hash the per-item trust gate binds
// to (trust rework, TR0): it covers the bytes actually exposed to the agent —
// never a raw fallback once distilled is served, and never the author-supplied
// ContentHash field. The form is provenance so a raw-form grant cannot validate a
// distilled exposure.
func (f *BundleFragment) EffectiveContentHash(preferDistilled bool) (string, ContentForm) {
	content, form := resolveEffective(preferDistilled, f.Content, f.Distilled, f.NoDistill)
	return hashContent([]byte(content)), form
}

// ComputeContentHash computes the SHA256 hash of the raw authored content. This
// feeds the recorded content_hash that drives re-distillation (NeedsDistill); the
// trust gate uses EffectiveContentHash instead.
func (p *BundleSkill) ComputeContentHash() string {
	return hashContent([]byte(p.Content))
}

// NeedsDistill returns true if this prompt needs distillation.
func (p *BundleSkill) NeedsDistill() bool {
	return staleDistill(p.NoDistill, p.Distilled, p.ContentHash, p.Content)
}

// EffectiveContent returns distilled content if available and preferred.
// Falls back to original content if distilled is empty or NoDistill is true.
func (p *BundleSkill) EffectiveContent(preferDistilled bool) string {
	content, _ := resolveEffective(preferDistilled, p.Content, p.Distilled, p.NoDistill)
	return content
}

// EffectiveContentHash hashes EXACTLY the bytes EffectiveContent(preferDistilled)
// returns, and reports their form. See BundleFragment.EffectiveContentHash — same
// contract for the per-item trust gate (trust rework, TR0).
func (p *BundleSkill) EffectiveContentHash(preferDistilled bool) (string, ContentForm) {
	content, form := resolveEffective(preferDistilled, p.Content, p.Distilled, p.NoDistill)
	return hashContent([]byte(content)), form
}

// ComputeContentHash hashes a canonical encoding of the MCP server's executable
// surface — Command, Args (order significant), Env (key-sorted), and Installation.
// Notes are excluded (human-only, never executed). encoding/json provides the
// determinism: it sorts map keys, so reordering Env yields an identical hash while
// reordering Args (a slice) does not. This is the hash an MCP trust grant binds to
// (trust rework, TR0); an MCP server has no distilled form, so there is one hash.
func (m *BundleMCP) ComputeContentHash() string {
	canonical := struct {
		Command      string            `json:"command"`
		Args         []string          `json:"args"`
		Env          map[string]string `json:"env"`
		Installation string            `json:"installation"`
	}{
		Command:      m.Command,
		Args:         m.Args,
		Env:          m.Env,
		Installation: m.Installation,
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		// Unreachable: the struct holds only strings/[]string/map[string]string,
		// none of which json.Marshal can fail on. Fail closed to a stable digest
		// rather than panic.
		return hashContent([]byte("ctxloom:mcp-content-hash-error"))
	}
	return hashContent(data)
}

// ComputeContentHash hashes a canonical encoding of the hook's executable
// surface — Matcher, Type, Command, Prompt, and the PreToolFallback flag: the
// fields that determine what runs and how it fires. Timeout/Async (operational
// knobs) and the firing event are excluded — the event is carried by the hook's
// id, and excluding it keeps the content-hash denylist event-agnostic so the
// same malicious command is blocked wherever it is wired. encoding/json provides
// the determinism (stable field order). This is the hash a bundle-hook trust
// grant binds to (trust rework, TR5); a hook has no distilled form, so there is
// one hash. Mirrors BundleMCP.ComputeContentHash.
func (h *BundleHook) ComputeContentHash() string {
	canonical := struct {
		Matcher         string `json:"matcher"`
		Type            string `json:"type"`
		Command         string `json:"command"`
		Prompt          string `json:"prompt"`
		PreToolFallback bool   `json:"pre_tool_fallback"`
	}{
		Matcher:         h.Matcher,
		Type:            h.Type,
		Command:         h.Command,
		Prompt:          h.Prompt,
		PreToolFallback: h.PreToolFallback,
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		// Unreachable (only strings + a bool); fail closed to a stable digest.
		return hashContent([]byte("ctxloom:hook-content-hash-error"))
	}
	return hashContent(data)
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
	return slices.Sorted(maps.Keys(b.MCP))
}

// FragmentCount returns the number of fragments in the bundle.
func (b *Bundle) FragmentCount() int {
	return len(b.Fragments)
}

// SkillCount returns the number of prompts in the bundle.
func (b *Bundle) SkillCount() int {
	return len(b.Skills)
}

// FragmentNames returns sorted fragment names.
func (b *Bundle) FragmentNames() []string {
	return slices.Sorted(maps.Keys(b.Fragments))
}

// PromptNames returns sorted prompt names.
func (b *Bundle) PromptNames() []string {
	return slices.Sorted(maps.Keys(b.Skills))
}

// HasProfiles reports whether the bundle ships any profiles.
func (b *Bundle) HasProfiles() bool {
	return len(b.Profiles) > 0
}

// ProfileCount returns the number of profiles in the bundle.
func (b *Bundle) ProfileCount() int {
	return len(b.Profiles)
}

// ProfileNames returns sorted profile names.
func (b *Bundle) ProfileNames() []string {
	return slices.Sorted(maps.Keys(b.Profiles))
}

// AllTags returns all unique tags from bundle and its contents.
func (b *Bundle) AllTags() []string {
	tagSet := collections.NewSet[string]()
	tagSet.AddAll(b.Tags...)
	for _, f := range b.Fragments {
		tagSet.AddAll(f.Tags...)
	}
	for _, p := range b.Skills {
		tagSet.AddAll(p.Tags...)
	}

	tags := tagSet.Items()
	sort.Strings(tags)
	return tags
}

// ParseBundle parses raw YAML into a Bundle.
func ParseBundle(data []byte) (*Bundle, error) {
	// Migrate older on-disk/remote bundle schemas (e.g. the legacy `prompts:`
	// key → `skills:`) in memory before unmarshal, so old bundles load instead
	// of silently dropping renamed keys. No-op for already-current bundles.
	if upgraded, applied := bundleUpgrades.Run(data); len(applied) > 0 {
		data = upgraded
	}

	var bundle Bundle
	if err := yaml.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("invalid bundle YAML: %w", err)
	}

	// Initialize maps if nil
	if bundle.Fragments == nil {
		bundle.Fragments = make(map[string]BundleFragment)
	}
	if bundle.Skills == nil {
		bundle.Skills = make(map[string]BundleSkill)
	}
	if bundle.MCP == nil {
		bundle.MCP = make(map[string]BundleMCP)
	}
	if bundle.Profiles == nil {
		bundle.Profiles = make(map[string]BundleProfile)
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
