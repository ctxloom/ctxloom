package bundles

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/collections"
	"github.com/ctxloom/ctxloom/internal/errs"
)

// LoadedContent is a fully resolved fragment or prompt with its bundle
// metadata, ready to assemble into context.
type LoadedContent struct {
	Name         string            // Full name (bundle/item)
	Version      string            // Bundle version
	Tags         []string          // Combined tags
	Content      string            // The actual content
	Installation string            // Setup/installation instructions for tooling
	IsDistilled  bool              // Whether distilled version was used
	DistilledBy  string            // Model that created distillation
	Exports      map[string]string // Exported variables (from generators)
	Plugins      PluginsConfig     // Plugin-specific settings
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

// GeminiConfig holds configuration for exporting prompts as Gemini CLI slash commands.
type GeminiConfig struct {
	Enabled     *bool  `yaml:"enabled"`     // nil = true (opt-out model)
	Description string `yaml:"description"` // For /help display
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c GeminiConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// LMPluginConfig holds LM plugin-specific settings.
type LMPluginConfig struct {
	ClaudeCode ClaudeCodeConfig `yaml:"claude-code"`
	Gemini     GeminiConfig     `yaml:"gemini"`
}

// PluginsConfig holds plugin-specific settings.
type PluginsConfig struct {
	LM LMPluginConfig `yaml:"llm"`
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
				Tags:     append(bundle.Tags, prompt.Tags...),
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
	// Check for # syntax: bundle#fragments/name
	if idx := strings.Index(name, "#"); idx != -1 {
		bundleName := name[:idx]
		itemPath := name[idx+1:]

		// Parse itemPath: "fragments/name"
		parts := strings.SplitN(itemPath, "/", 2)
		if len(parts) != 2 || parts[0] != "fragments" {
			return nil, fmt.Errorf("invalid fragment reference: %s", name)
		}
		fragName := parts[1]

		bundle, err := l.Load(bundleName)
		if err != nil {
			return nil, err
		}

		frag, ok := bundle.Fragments[fragName]
		if !ok {
			return nil, fmt.Errorf("fragment %q not found in bundle %q", fragName, bundleName)
		}

		return &LoadedContent{
			Name:         fmt.Sprintf("%s/%s", bundle.Name, fragName),
			Version:      bundle.Version,
			Tags:         append(bundle.Tags, frag.Tags...),
			Content:      frag.EffectiveContent(l.preferDistilled),
			Installation: frag.Installation,
			IsDistilled:  l.preferDistilled && frag.Distilled != "",
			DistilledBy:  frag.DistilledBy,
		}, nil
	}

	// Search all bundles for the fragment
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		if frag, ok := bundle.Fragments[name]; ok {
			return &LoadedContent{
				Name:         fmt.Sprintf("%s/%s", bundle.Name, name),
				Version:      bundle.Version,
				Tags:         append(bundle.Tags, frag.Tags...),
				Content:      frag.EffectiveContent(l.preferDistilled),
				Installation: frag.Installation,
				IsDistilled:  l.preferDistilled && frag.Distilled != "",
				DistilledBy:  frag.DistilledBy,
			}, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errs.ErrFragmentNotFound, name)
}

// GetPrompt finds and loads a prompt by name.
// Name can be "prompt-name" (searches all bundles) or "bundle#prompts/name".
func (l *Loader) GetPrompt(name string) (*LoadedContent, error) {
	// Check for # syntax: bundle#prompts/name
	if idx := strings.Index(name, "#"); idx != -1 {
		bundleName := name[:idx]
		itemPath := name[idx+1:]

		// Parse itemPath: "prompts/name"
		parts := strings.SplitN(itemPath, "/", 2)
		if len(parts) != 2 || parts[0] != "prompts" {
			return nil, fmt.Errorf("invalid prompt reference: %s", name)
		}
		promptName := parts[1]

		bundle, err := l.Load(bundleName)
		if err != nil {
			return nil, err
		}

		prompt, ok := bundle.Prompts[promptName]
		if !ok {
			return nil, fmt.Errorf("prompt %q not found in bundle %q", promptName, bundleName)
		}

		return &LoadedContent{
			Name:         fmt.Sprintf("%s/%s", bundle.Name, promptName),
			Version:      bundle.Version,
			Tags:         append(bundle.Tags, prompt.Tags...),
			Content:      prompt.EffectiveContent(l.preferDistilled),
			Installation: prompt.Installation,
			IsDistilled:  l.preferDistilled && prompt.Distilled != "",
			DistilledBy:  prompt.DistilledBy,
			Plugins:      prompt.Plugins,
		}, nil
	}

	// Search all bundles for the prompt
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		if prompt, ok := bundle.Prompts[name]; ok {
			return &LoadedContent{
				Name:         fmt.Sprintf("%s/%s", bundle.Name, name),
				Version:      bundle.Version,
				Tags:         append(bundle.Tags, prompt.Tags...),
				Content:      prompt.EffectiveContent(l.preferDistilled),
				Installation: prompt.Installation,
				IsDistilled:  l.preferDistilled && prompt.Distilled != "",
				DistilledBy:  prompt.DistilledBy,
				Plugins:      prompt.Plugins,
			}, nil
		}
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
		for _, t := range info.Tags {
			if tagSet.Has(t) {
				matched = append(matched, info)
				break
			}
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
// is reproducible.
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
	// Use IndexAny so we accept either separator. The bundle name itself may
	// contain "/" (e.g. "remote/bundle") but never ":" or "#".
	if idx := strings.IndexAny(ref, ":#"); idx != -1 {
		bundleName := ref[:idx]
		rest := ref[idx+1:]
		if !strings.HasPrefix(rest, "fragments/") {
			// Targeted at prompts, mcp, or unknown — not a fragment ref.
			return nil
		}
		return []string{bundleName + "#" + rest}
	}

	// Whole-bundle ref: enumerate every fragment in the bundle.
	b, err := l.Load(ref)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(b.Fragments))
	for fragName := range b.Fragments {
		names = append(names, ref+"#fragments/"+fragName)
	}
	sort.Strings(names)
	return names
}
