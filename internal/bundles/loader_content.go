package bundles

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/shared/collections"
)

// LoadedContent is a fully resolved fragment or prompt with its bundle
// metadata, ready to assemble into context.
type LoadedContent struct {
	Name         string            // Full name (bundle/item)
	Bundle       string            // Owning bundle's loader name (canonical ref for remote bundles)
	Item         string            // Bare fragment/prompt name within the bundle
	Version      string            // Bundle version
	Tags         []string          // Combined tags
	Content      string            // The actual content
	Installation string            // Setup/installation instructions for tooling
	IsDistilled  bool              // Whether distilled version was used
	DistilledBy  string            // Model that created distillation
	Exports      map[string]string // Exported variables (from generators)
	LLM          LLMExports        // Per-LLM export settings (slash-command config)
}

// ExportName returns the short, slash-command-facing name for this item:
// the owning bundle's last path segment plus the bare item name. Remote
// bundles are keyed by canonical ref ("<url>@bundles/<path>"); exporting
// that verbatim names the command after the entire URL (and ':' makes the
// filename invalid on Windows). Name remains the full identity — only the
// export-facing name shortens. Content without bundle metadata (builtin
// prompts) falls back to Name.
func (c *LoadedContent) ExportName() string {
	if c.Bundle == "" || c.Item == "" {
		return c.Name
	}
	return exportBaseName(c.Bundle) + "/" + c.Item
}

// exportBaseName shortens a bundle loader name to its last path segment,
// stripping the canonical ref's "<url>@<type>/" prefix when present.
func exportBaseName(bundleName string) string {
	base := bundleName
	if i := strings.LastIndex(base, "@"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
}

// ClaudeCodeConfig holds configuration for exporting prompts as Claude Code slash commands.
type ClaudeCodeConfig struct {
	Enabled      *bool    `yaml:"enabled"`       // nil = true (opt-out model)
	Description  string   `yaml:"description"`   // For /help display
	ArgumentHint string   `yaml:"argument_hint"` // Autocomplete hint
	AllowedTools []string `yaml:"allowed_tools"` // Tool restrictions
	Model        string   `yaml:"model"`         // Override model
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c ClaudeCodeConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// AntigravityConfig holds configuration for exporting prompts as Antigravity
// CLI (agy) skill files.
type AntigravityConfig struct {
	Enabled     *bool  `yaml:"enabled"`     // nil = true (opt-out model)
	Description string `yaml:"description"` // For skill listings
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c AntigravityConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// CodexConfig holds configuration for exporting prompts as Codex CLI custom prompts.
type CodexConfig struct {
	Enabled      *bool  `yaml:"enabled"`       // nil = true (opt-out model)
	Description  string `yaml:"description"`   // For the /prompts listing
	ArgumentHint string `yaml:"argument_hint"` // Autocomplete hint
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c CodexConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// LLMExports holds per-LLM export settings for a fragment/prompt — e.g. how it
// surfaces as a slash command in each backend — keyed by backend name.
type LLMExports struct {
	ClaudeCode  ClaudeCodeConfig  `yaml:"claude-code"`
	Antigravity AntigravityConfig `yaml:"antigravity"`
	Codex       CodexConfig       `yaml:"codex"`
}

// ContentInfo provides metadata about a fragment or prompt for listing.
type ContentInfo struct {
	Name     string
	FileName string
	Path     string
	Source   string // "bundle:name" or legacy path
	Tags     []string
	Bundle   string // Bundle name this came from
	ItemType string // "fragment" or "prompt"
}

// ListAllFragments returns info about all fragments across all bundles.
func (l *Loader) ListAllFragments() ([]ContentInfo, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	var infos []ContentInfo
	seen := collections.NewSet[string]()

	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		for name, frag := range bundle.Fragments {
			// Use bundleInfo.Name (full path) instead of bundle.Name (just filename)
			key := bundleInfo.Name + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:     name,
				FileName: name + ".yaml",
				Path:     bundleInfo.Path,
				Source:   bundleInfo.Name,
				Tags:     slices.Concat(bundle.Tags, frag.Tags),
				Bundle:   bundleInfo.Name,
				ItemType: "fragment",
			})
		}
	}

	return infos, nil
}

// ListAllPrompts returns info about all prompts across all bundles.
func (l *Loader) ListAllPrompts() ([]ContentInfo, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	seen := collections.NewSet[string]()
	var infos []ContentInfo
	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		for name, prompt := range bundle.Prompts {
			// Use bundleInfo.Name (normalized full path) instead of bundle.Name (just filename)
			key := bundleInfo.Name + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:     name,
				FileName: name + ".yaml",
				Path:     bundleInfo.Path,
				Source:   bundleInfo.Name,
				Tags:     slices.Concat(bundle.Tags, prompt.Tags),
				Bundle:   bundleInfo.Name,
				ItemType: "prompt",
			})
		}
	}

	return infos, nil
}

// GetFragment finds and loads a fragment by name.
// Name can be "fragment-name" (searches all bundles) or "bundle#fragments/name".
func (l *Loader) GetFragment(name string) (*LoadedContent, error) {
	bundleName, fragName, isRef, err := splitItemRef(name, "fragments")
	if err != nil {
		return nil, err
	}
	if isRef {
		return l.fragmentFromBundle(bundleName, fragName)
	}
	return l.searchFragment(name)
}

// splitItemRef parses a "bundle#kind/name" reference. isRef reports whether a
// "#" was present at all; when it was, kind must equal want or an error is
// returned. For a plain name (no "#"), isRef is false and the caller searches.
func splitItemRef(name, want string) (bundleName, itemName string, isRef bool, err error) {
	bundleName, rest, found := strings.Cut(name, "#")
	if !found {
		return "", "", false, nil
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] != want {
		return "", "", true, fmt.Errorf("invalid %s reference: %s", strings.TrimSuffix(want, "s"), name)
	}
	return bundleName, parts[1], true, nil
}

// fragmentContent builds a LoadedContent for a fragment, or returns nil when the
// trust gate withholds it (trust rework, TR5). The gate hashes the EXACT
// effective-content bytes this returns (pre-mustache), so the decision keys on
// what the agent would actually see.
func (l *Loader) fragmentContent(bundle *Bundle, fragName string, frag BundleFragment) *LoadedContent {
	content := frag.EffectiveContent(l.preferDistilled)
	hash, form := frag.EffectiveContentHash(l.preferDistilled)
	if !l.gateContent(bundle.Name, "fragments", fragName, hash, form) {
		return nil
	}
	return &LoadedContent{
		Name:         fmt.Sprintf("%s/%s", bundle.Name, fragName),
		Bundle:       bundle.Name,
		Item:         fragName,
		Version:      bundle.Version,
		Tags:         slices.Concat(bundle.Tags, frag.Tags),
		Content:      content,
		Installation: frag.Installation,
		IsDistilled:  l.preferDistilled && frag.Distilled != "",
		DistilledBy:  frag.DistilledBy,
	}
}

// fragmentFromBundle loads a specific bundle and returns the named fragment.
func (l *Loader) fragmentFromBundle(bundleName, fragName string) (*LoadedContent, error) {
	bundle, err := l.Load(bundleName)
	if err != nil {
		return nil, err
	}
	frag, ok := bundle.Fragments[fragName]
	if !ok {
		return nil, fmt.Errorf("fragment %q not found in bundle %q", fragName, bundleName)
	}
	lc := l.fragmentContent(bundle, fragName, frag)
	if lc == nil {
		return nil, fmt.Errorf("%w: %s", errs.ErrFragmentWithheld, fragName)
	}
	return lc, nil
}

// ResolveFragmentAsk resolves a user-supplied fragment ask to the canonical
// name shape the assembly pipeline carries (see ExpandBundleRefs). A
// qualified ask ("bundle#fragments/name") canonicalizes its bundle part
// directly. A bare name searches all bundles: a unique match qualifies it;
// several matches resolve deterministically to the first in List order (List
// sorts by bundle name) with a warning naming the alternatives; no match
// returns the ask unchanged so the load step reports it (fault-tolerance:
// an explicit ask is never dropped silently).
func (l *Loader) ResolveFragmentAsk(name string) string {
	if strings.Contains(name, "#") {
		return remote.CanonicalFragmentRef(name)
	}
	bundleInfos, err := l.List()
	if err != nil {
		return name
	}
	var matches []string
	for _, bundleInfo := range bundleInfos {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}
		if _, ok := bundle.Fragments[name]; ok {
			matches = append(matches, remote.CanonicalBundleRef(bundleInfo.Name))
		}
	}
	if len(matches) == 0 {
		return name
	}
	if len(matches) > 1 {
		unresolvedBundleWarner.ambiguous(name, matches, matches[0])
	}
	return matches[0] + remote.FragmentSelector + name
}

// searchFragment scans every bundle for a fragment with the given name. A match
// the trust gate withholds (trust rework, TR5) does not end the scan — a trusted
// copy in another bundle still wins; only when every match is withheld does it
// report ErrFragmentWithheld (distinct from not-found).
func (l *Loader) searchFragment(name string) (*LoadedContent, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}
	withheld := false
	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}
		if frag, ok := bundle.Fragments[name]; ok {
			if lc := l.fragmentContent(bundle, name, frag); lc != nil {
				return lc, nil
			}
			withheld = true
		}
	}
	if withheld {
		return nil, fmt.Errorf("%w: %s", errs.ErrFragmentWithheld, name)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrFragmentNotFound, name)
}

// GetPrompt finds and loads a prompt by name.
// Name can be "prompt-name" (searches all bundles) or "bundle#prompts/name".
func (l *Loader) GetPrompt(name string) (*LoadedContent, error) {
	bundleName, promptName, isRef, err := splitItemRef(name, "prompts")
	if err != nil {
		return nil, err
	}
	if isRef {
		return l.promptFromBundle(bundleName, promptName)
	}
	return l.searchPrompt(name)
}

// promptContent builds a LoadedContent for a prompt (prompts also carry Plugins),
// or returns nil when the trust gate withholds it (trust rework, TR5). See
// fragmentContent — the gate hashes the exact effective-content bytes returned.
func (l *Loader) promptContent(bundle *Bundle, promptName string, prompt BundlePrompt) *LoadedContent {
	content := prompt.EffectiveContent(l.preferDistilled)
	hash, form := prompt.EffectiveContentHash(l.preferDistilled)
	if !l.gateContent(bundle.Name, "prompts", promptName, hash, form) {
		return nil
	}
	return &LoadedContent{
		Name:         fmt.Sprintf("%s/%s", bundle.Name, promptName),
		Bundle:       bundle.Name,
		Item:         promptName,
		Version:      bundle.Version,
		Tags:         slices.Concat(bundle.Tags, prompt.Tags),
		Content:      content,
		Installation: prompt.Installation,
		IsDistilled:  l.preferDistilled && prompt.Distilled != "",
		DistilledBy:  prompt.DistilledBy,
		LLM:          prompt.LLM,
	}
}

// promptFromBundle loads a specific bundle and returns the named prompt.
func (l *Loader) promptFromBundle(bundleName, promptName string) (*LoadedContent, error) {
	bundle, err := l.Load(bundleName)
	if err != nil {
		return nil, err
	}
	prompt, ok := bundle.Prompts[promptName]
	if !ok {
		return nil, fmt.Errorf("prompt %q not found in bundle %q", promptName, bundleName)
	}
	lc := l.promptContent(bundle, promptName, prompt)
	if lc == nil {
		return nil, fmt.Errorf("%w: %s", errs.ErrPromptWithheld, promptName)
	}
	return lc, nil
}

// searchPrompt scans every bundle for a prompt with the given name. A gate-
// withheld match (trust rework, TR5) does not end the scan; only when every
// match is withheld does it report ErrPromptWithheld (distinct from not-found).
func (l *Loader) searchPrompt(name string) (*LoadedContent, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}
	withheld := false
	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}
		if prompt, ok := bundle.Prompts[name]; ok {
			if lc := l.promptContent(bundle, name, prompt); lc != nil {
				return lc, nil
			}
			withheld = true
		}
	}
	if withheld {
		return nil, fmt.Errorf("%w: %s", errs.ErrPromptWithheld, name)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrPromptNotFound, name)
}

// ListByTags returns fragments matching any of the given tags.
func (l *Loader) ListByTags(tags []string) ([]ContentInfo, error) {
	all, err := l.ListAllFragments()
	if err != nil {
		return nil, err
	}

	tagSet := collections.NewSetFrom(tags...)

	var matched []ContentInfo
	for _, info := range all {
		if slices.ContainsFunc(info.Tags, tagSet.Has) {
			matched = append(matched, info)
		}
	}

	return matched, nil
}

// LoadMultiple loads multiple fragments by name and returns combined content.
// Returns the content, the names of fragments that were successfully loaded, and any error.
func (l *Loader) LoadMultiple(names []string) (string, []string, error) {
	var parts []string
	var loaded []string

	for _, name := range names {
		content, err := l.GetFragment(name)
		if err != nil {
			// Skip not found, continue with others
			continue
		}
		parts = append(parts, strings.TrimSpace(content.Content))
		loaded = append(loaded, name)
	}

	return strings.Join(parts, "\n\n---\n\n"), loaded, nil
}

// ExpandBundleRefs expands profile bundle references into canonical fragment
// names usable with GetFragment. See the Profile.Bundles documentation in
// internal/profiles for the supported reference syntax.
//
// Supported reference forms:
//
//	"bundle"                        // every fragment in the bundle
//	"bundle#fragments/name"         // a single fragment (canonical syntax)
//	"bundle:fragments/name"         // a single fragment (profile syntax alias)
//
// Refs that target prompts or MCP servers (e.g. "bundle:prompts/x",
// "bundle:mcp") are skipped, because they do not resolve to fragments.
// Bundles that cannot be loaded are also skipped, mirroring the tolerant
// behavior of LoadMultiple/GetFragment so a missing bundle does not abort
// the whole assembly.
//
// The returned names are deduplicated and stable: whole-bundle expansions
// are sorted alphabetically by fragment name so the resulting context hash
// is reproducible. Bundle identities are canonicalized
// (remote.CanonicalBundleRef) — remote refs to their version-less canonical
// URL, plain local names to ctxloom:local form — so names from different
// reference spellings of the same bundle compare and dedupe exactly.
func (l *Loader) ExpandBundleRefs(refs []string) []string {
	seen := collections.NewSet[string]()
	var out []string
	for _, ref := range refs {
		for _, name := range l.expandBundleRef(ref) {
			if seen.Has(name) {
				continue
			}
			seen.Add(name)
			out = append(out, name)
		}
	}
	return out
}

// expandBundleRef returns the canonical fragment names for a single ref.
// See ExpandBundleRefs for the supported syntax.
func (l *Loader) expandBundleRef(ref string) []string {
	if ref == "" {
		return nil
	}

	// Targeted ref: bundle{:|#}{fragments|prompts|mcp}/...
	// Locate the item selector WITHOUT tripping on a source's scheme colon.
	// '#' is the canonical separator and is unambiguous — a ref never contains
	// '#' except to introduce a selector (canonical URLs included). The ':'
	// alias counts only when it introduces a known section, so a URL scheme's
	// ':' (always followed by "//") is never mistaken for a selector. (The
	// previous IndexAny(":#") split a "https://…" ref on the scheme colon,
	// dropping every URL-form cherry-pick.)
	sep := strings.Index(ref, "#")
	if sep == -1 {
		for _, marker := range []string{":fragments/", ":prompts/", ":mcp"} {
			if i := strings.Index(ref, marker); i != -1 {
				sep = i
				break
			}
		}
	}
	if sep != -1 {
		bundleName := ref[:sep]
		rest := ref[sep+1:]
		if !strings.HasPrefix(rest, "fragments/") {
			// Targeted at prompts, mcp, or unknown — not a fragment ref.
			return nil
		}
		return []string{remote.CanonicalBundleRef(bundleName) + "#" + rest}
	}

	// Whole-bundle ref: enumerate every fragment in the bundle.
	b, err := l.Load(ref)
	if err != nil {
		// A profile referenced this bundle but it didn't resolve. Warn so the
		// gap is diagnosable — silently dropping it produces context that is
		// missing content with no error (fault-tolerance: log, don't crash).
		// Deduped process-wide: startup assembles context more than once.
		unresolvedBundleWarner.unresolved(ref, err)
		return nil
	}
	names := make([]string, 0, len(b.Fragments))
	canonical := remote.CanonicalBundleRef(ref)
	for fragName := range b.Fragments {
		names = append(names, canonical+remote.FragmentSelector+fragName)
	}
	sort.Strings(names)
	return names
}
