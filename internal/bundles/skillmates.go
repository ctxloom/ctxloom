package bundles

import (
	"slices"
	"sort"
)

// UninvokedSkillMates answers the question a link group asks at a skill's
// COMPLETION: which of its group-mates has the session not invoked yet?
//
// name, and every name returned, is the engine-facing identity: the SKILL.md
// frontmatter name every engine materializes a package under
// (engine.BuildSkillExports) and the name a Skill tool call carries. Link
// membership is read off each delivered skill's effective tags (LinkIDs over
// the bundle+item merge the loader already did), keyed by (bundle, id) exactly
// as LinkGroups keys it -- so two bundles that both say `nightly` are two
// groups, and one author's skill never points at another's.
//
// Only SKILL mates count: a fragment or command in the group is delivered
// beside the skill (or withheld with it) and has no invocation to wait for. A
// skill in two groups gets the union. The invoked skill is never its own mate,
// and invoked mates are dropped. Sorted and deduplicated; nil when there is
// nothing to say, which is the common case.
func UninvokedSkillMates(delivered []*LoadedSkill, name string, invoked func(string) bool) []string {
	seen := make(map[string]bool)
	var mates []string
	for _, s := range delivered {
		if s == nil || s.Frontmatter.Name != name {
			continue
		}
		ids := LinkIDs(s.Tags)
		for _, o := range delivered {
			if o == nil || o == s || o.Bundle != s.Bundle {
				continue
			}
			mate := o.Frontmatter.Name
			if mate == "" || mate == name || seen[mate] {
				continue
			}
			if !slices.ContainsFunc(LinkIDs(o.Tags), func(id string) bool { return slices.Contains(ids, id) }) {
				continue
			}
			if invoked(mate) {
				continue
			}
			seen[mate] = true
			mates = append(mates, mate)
		}
	}
	sort.Strings(mates)
	return mates
}
