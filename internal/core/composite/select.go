package composite

import (
	"fmt"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
)

// Select resolves what a profile set asks for over a catalog. resolved are
// the profiles already loaded and inheritance-merged, in the order asked;
// req is the caller's explicit arm. The result is a value: the assembly
// order of fragments and everything else the set declares, canonicalised.
//
// Per profile, in order: the tag matches, the direct fragment asks, then
// the whole-bundle expansions (each bundle's fragments by name), each
// filtered by that profile's exclusions. After every profile: the caller's
// named asks (resolved to their qualified identity, recorded as Explicit)
// and the caller's tag matches. One entry per item survives — the highest
// priority any ask gave it, an explicit "@<commit>" over the default — in
// first-occurrence order, then bookended: highest priority first,
// second-highest last, the rest between in that order.
func Select(resolved []profiles.ResolvedProfile, cat bundles.Catalog, req SelectRequest) (Selection, error) {
	sel := Selection{
		Exclusions: map[string]struct{}{},
		Variables:  map[string]string{},
		Declared:   map[string][]string{},
	}
	loader := bundles.LoaderOf(cat)
	if req.Versions != nil {
		loader.WithVersionResolver(req.Versions, req.VersionRoot)
	}
	var asks []FragmentAsk
	seenBundle := map[string]bool{}
	seenDeny := map[string]bool{}
	seenAsk := map[string]bool{}

	for _, p := range resolved {
		sel.Profiles = append(sel.Profiles, p.Name)
		for k, v := range p.Variables {
			sel.Variables[k] = v
		}
		if sel.LLM == "" {
			sel.LLM = p.LLM
		}
		for _, tool := range p.DenyTools {
			if !seenDeny[tool] {
				seenDeny[tool] = true
				sel.DenyTools = append(sel.DenyTools, tool)
			}
		}
		for _, server := range p.ExcludeMCP {
			sel.Exclusions[server] = struct{}{}
		}
		for _, ref := range p.Commands {
			if !seenAsk["command:"+ref] {
				seenAsk["command:"+ref] = true
				sel.Commands = append(sel.Commands, ItemAsk{Ref: ref})
			}
		}
		for _, ref := range p.Skills {
			if !seenAsk["skill:"+ref] {
				seenAsk["skill:"+ref] = true
				sel.Skills = append(sel.Skills, ItemAsk{Ref: ref})
			}
		}
		for _, ref := range p.Bundles {
			if !seenBundle[ref] {
				seenBundle[ref] = true
				sel.Bundles = append(sel.Bundles, ref)
			}
		}
		if p.Hooks.HasAny() {
			sel.Hooks = append(sel.Hooks, ProfileHooks{Profile: p.Name, SourceRef: p.SourceRef, Signer: p.Signer, Hooks: p.Hooks})
		}

		excluded := bundles.NewExclusions(p.ExcludeFragments)
		declare := func(ask FragmentAsk) {
			if excluded.Excludes(ask.Name) {
				return
			}
			asks = append(asks, ask)
			sel.Declared[p.Name] = append(sel.Declared[p.Name], ask.Name)
		}
		tagged, err := fragmentsByTags(cat, p.SelectTags)
		if err != nil {
			return Selection{}, fmt.Errorf("composite: profile tags: %w", err)
		}
		for _, ask := range tagged {
			declare(ask)
		}
		for _, f := range p.Fragments {
			name, version, err := bundles.SplitFragmentVersion(f.Name)
			if err != nil {
				// Withheld, not fatal: one unaddressable ref must not cost the
				// profile every other fragment it declares. It reaches the load
				// step as authored, which reports the gap.
				name, version = f.Name, ""
			}
			declare(FragmentAsk{Name: name, Version: version, Priority: f.Priority})
		}
		for _, er := range loader.ExpandBundleRefs(slices.Concat(p.Bundles, p.BundleItems)) {
			declare(FragmentAsk{Name: er.Name, Version: er.Version})
		}
	}

	for _, f := range req.Fragments {
		name := cat.ResolveFragmentAsk(f)
		sel.Explicit = append(sel.Explicit, name)
		asks = append(asks, FragmentAsk{Name: name})
	}
	tagged, err := fragmentsByTags(cat, req.Tags)
	if err != nil {
		return Selection{}, fmt.Errorf("composite: tags: %w", err)
	}
	asks = append(asks, tagged...)
	sel.Tags = append([]string(nil), req.Tags...)
	if len(req.Tags) > 0 && len(tagged) == 0 {
		sel.MissingTags = append([]string(nil), req.Tags...)
	}

	sel.Fragments = bookend(dedupe(asks))
	return sel, nil
}

// fragmentsByTags is every fragment in the catalog carrying any of tags, as
// asks at priority 0, in listing order. A fragment whose bundle will not
// canonicalise costs itself, never the query.
func fragmentsByTags(cat bundles.Catalog, tags []string) ([]FragmentAsk, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	infos, err := cat.ByTags(tags)
	if err != nil {
		return nil, err
	}
	asks := make([]FragmentAsk, 0, len(infos))
	for _, info := range infos {
		name, _, err := bundles.SplitFragmentVersion(info.Bundle + bundles.FragmentSelector + info.Name)
		if err != nil {
			continue
		}
		asks = append(asks, FragmentAsk{Name: name})
	}
	return asks, nil
}

// dedupe keeps one ask per name: the highest priority any ask gave it, an
// explicit version over the default, in first-occurrence order.
func dedupe(asks []FragmentAsk) []FragmentAsk {
	index := map[string]int{}
	var out []FragmentAsk
	for _, a := range asks {
		i, seen := index[a.Name]
		if !seen {
			index[a.Name] = len(out)
			out = append(out, a)
			continue
		}
		if a.Priority > out[i].Priority {
			out[i].Priority = a.Priority
		}
		if out[i].Version == "" && a.Version != "" {
			out[i].Version = a.Version
		}
	}
	return out
}

// bookend places the highest priority first and the second-highest last,
// the rest between in descending priority (stable): the "lost in the
// middle" placement. One or two asks are simply in priority order.
func bookend(asks []FragmentAsk) []FragmentAsk {
	if len(asks) == 0 {
		return nil
	}
	sorted := slices.Clone(asks)
	slices.SortStableFunc(sorted, func(a, b FragmentAsk) int { return b.Priority - a.Priority })
	if len(sorted) <= 2 {
		return sorted
	}
	out := make([]FragmentAsk, 0, len(sorted))
	out = append(out, sorted[0])
	out = append(out, sorted[2:]...)
	out = append(out, sorted[1])
	return out
}
