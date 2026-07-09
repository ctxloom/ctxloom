package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// SkillEntry represents a prompt in operation results. Tags carry the
// bundle's tags merged with the prompt's own; Source is the bundle name,
// which also serves as the grouping key for the CLI's grouped listing.
type SkillEntry struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags,omitempty"`
	Source string   `json:"source"`
}

// ListSkillsRequest contains parameters for listing prompts.
type ListSkillsRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // "name" or "source"
	SortOrder string `json:"sort_order"` // "asc" or "desc"

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// ListSkillsResult contains the list of skills.
type ListSkillsResult struct {
	Skills []SkillEntry `json:"skills"`
	Count  int          `json:"count"`
}

// ListSkills returns all prompts matching the criteria. Mirrors
// ListFragments: it filters and sorts the raw ContentInfo (so SortBy:"source"
// groups by bundle) before projecting, keeping one read path for both the MCP
// resource surface and the grouped CLI listing.
func ListSkills(ctx context.Context, cfg *config.Config, req ListSkillsRequest) (*ListSkillsResult, error) {
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	infos, err := loader.ListAllSkills()
	if err != nil {
		return nil, err
	}

	// Filter by query if provided (name match, matching the prior behavior).
	if req.Query != "" {
		query := strings.ToLower(req.Query)
		var filtered []bundles.ContentInfo
		for _, info := range infos {
			if strings.Contains(strings.ToLower(info.Name), query) {
				filtered = append(filtered, info)
			}
		}
		infos = filtered
	}

	sortContentInfos(infos, req.SortBy, req.SortOrder)

	result := &ListSkillsResult{
		Skills: make([]SkillEntry, 0, len(infos)),
		Count:  len(infos),
	}
	for _, info := range infos {
		result.Skills = append(result.Skills, SkillEntry{
			Name:   info.Name,
			Tags:   info.Tags,
			Source: info.Source,
		})
	}

	return result, nil
}

// GetSkillRequest contains parameters for getting a prompt.
type GetSkillRequest struct {
	Name string `json:"name"`

	// Version optionally pins the prompt to a historical content version
	// ("@<commit>"). When empty, GetSkill parses any "@<commit>" trailing Name
	// itself (the name-addressed form `<bundle>#skills/<name>@<commit>`); a set
	// Version wins (mirroring how FragmentRef.Version threads a pin). The pinned
	// version resolves via the loader's GetPromptAtVersion, gated by ITS OWN
	// content hash; an unversioned ref takes today's GetSkill path unchanged.
	Version string `json:"version,omitempty"`

	// Loader is an optional pre-configured loader (for testing).
	Loader *bundles.Loader `json:"-"`
}

// GetSkillResult contains the prompt content.
type GetSkillResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// GetSkill returns a specific prompt by name.
func GetSkill(ctx context.Context, cfg *config.Config, req GetSkillRequest) (*GetSkillResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	loader := req.Loader
	if loader == nil {
		// Exposure surface (ctxloom://skills/{name}): gate the resolved content
		// (trust rework, TR5). A withheld skill surfaces as errs.ErrSkillWithheld
		// so the resource omits it.
		loader = exposureLoader(cfg)
	}

	// A name-addressed ref may pin a content version ("@<commit>"): split it to
	// the canonical version-less ref + parsed version. An explicit req.Version
	// wins over the parsed one (mirrors FragmentRef.Version threading). With no
	// version the unchanged GetSkill path resolves the lockfile-pinned default;
	// a pinned ref resolves that exact historical version via GetPromptAtVersion,
	// gated by ITS OWN content hash (fail-closed on fetch/resolve error).
	ref, version := remote.SplitPromptVersion(req.Name)
	if req.Version != "" {
		version = req.Version
	}
	prompt, err := getPromptVersioned(loader, req.Name, ref, version)
	if err != nil {
		return nil, err
	}

	// Clean content: drop a single leading H1 title line. Skip any leading
	// blank lines first (so a leading newline doesn't bypass stripping), then
	// remove exactly the first heading line — not a contiguous run of headings,
	// which would swallow real body sub-headings.
	lines := strings.Split(prompt.Content, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
		i++
	}
	content := strings.TrimSpace(strings.Join(lines[i:], "\n"))

	return &GetSkillResult{
		Name:    prompt.Name,
		Content: content,
	}, nil
}

// getPromptVersioned resolves a prompt honoring a pinned content version. An
// unversioned request takes the lockfile-pinned default path (loader.GetSkill
// on the ORIGINAL name, so today's behavior is unchanged); a "@<commit>"-pinned
// request resolves that exact historical version (loader.GetPromptAtVersion on
// the canonical version-less ref), gated by ITS OWN effective-content hash. A
// version fetch/resolve failure fails closed (returns the error so the surface
// withholds), mirroring loadFragmentRef.
func getPromptVersioned(loader *bundles.Loader, name, ref, version string) (*bundles.LoadedContent, error) {
	if version == "" {
		return loader.GetSkill(name)
	}
	return loader.GetPromptAtVersion(ref, version)
}
