package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// deliveredSkill is a LoadedSkill as the loader emits it: bundle+item tags
// already merged, frontmatter name carried verbatim.
func deliveredSkill(bundle, item, frontmatterName string, tags ...string) *LoadedSkill {
	return &LoadedSkill{
		Name:        bundle + "/" + item,
		Bundle:      bundle,
		Item:        item,
		Frontmatter: SkillFrontmatter{Name: frontmatterName},
		Tags:        tags,
	}
}

func never(string) bool { return false }

func invokedSet(names ...string) func(string) bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(n string) bool { return set[n] }
}

// TestUninvokedSkillMates_NamesTheUninvokedMate is the measured miss: the first
// skill of a consequent pair fires and the second is never reached. Its mate is
// named.
//
// MUTATION -- return nil unconditionally -- turns this red.
func TestUninvokedSkillMates_NamesTheUninvokedMate(t *testing.T) {
	delivered := []*LoadedSkill{
		deliveredSkill("ops", "admit", "admit", "ctxloom:link_id=nightly"),
		deliveredSkill("ops", "unattended", "unattended", "ctxloom:link_id=nightly"),
	}
	assert.Equal(t, []string{"unattended"}, UninvokedSkillMates(delivered, "admit", never))
}

// TestUninvokedSkillMates_SilentWhenEveryMateWasInvoked pins the other
// polarity: once the pair has both fired there is nothing left to point at.
//
// MUTATION -- invert the invoked check (`if invoked(...)` -> `if !invoked(...)`)
// -- turns this red AND NamesTheUninvokedMate red.
func TestUninvokedSkillMates_SilentWhenEveryMateWasInvoked(t *testing.T) {
	delivered := []*LoadedSkill{
		deliveredSkill("ops", "admit", "admit", "ctxloom:link_id=nightly"),
		deliveredSkill("ops", "unattended", "unattended", "ctxloom:link_id=nightly"),
	}
	assert.Nil(t, UninvokedSkillMates(delivered, "admit", invokedSet("unattended")))
}

// TestUninvokedSkillMates_SilentForAnUnlinkedSkill: a skill in no group has no
// mates, however many linked skills sit beside it.
//
// MUTATION -- make the link-id intersection always true -- turns this red: every
// same-bundle skill would be named.
func TestUninvokedSkillMates_SilentForAnUnlinkedSkill(t *testing.T) {
	delivered := []*LoadedSkill{
		deliveredSkill("ops", "admit", "admit", "ctxloom:link_id=nightly"),
		deliveredSkill("ops", "unattended", "unattended", "ctxloom:link_id=nightly"),
		deliveredSkill("ops", "free", "free"),
	}
	assert.Nil(t, UninvokedSkillMates(delivered, "free", never))
}

// TestUninvokedSkillMates_UnionsTwoGroups: a skill in TWO groups (closeout ->
// prompt-human, and closeout -> check-triggers under another id) names the
// uninvoked mates of both, once each, in name order.
//
// MUTATION -- return after the first matching group -- turns this red.
func TestUninvokedSkillMates_UnionsTwoGroups(t *testing.T) {
	delivered := []*LoadedSkill{
		deliveredSkill("ops", "closeout", "closeout", "ctxloom:link_id=wrap", "ctxloom:link_id=review"),
		deliveredSkill("ops", "prompt-human", "prompt-human", "ctxloom:link_id=wrap", "ctxloom:link_id=review"),
		deliveredSkill("ops", "check-triggers", "check-triggers", "ctxloom:link_id=review"),
		deliveredSkill("ops", "admit", "admit", "ctxloom:link_id=nightly"),
	}
	assert.Equal(t, []string{"check-triggers", "prompt-human"}, UninvokedSkillMates(delivered, "closeout", never))
	assert.Equal(t, []string{"check-triggers"}, UninvokedSkillMates(delivered, "closeout", invokedSet("prompt-human")))
}

// TestUninvokedSkillMates_SpeaksTheEngineFacingName pins the join this
// function exists for: membership is keyed by the bundle.yaml item key, the
// engine invokes and materializes by the SKILL.md frontmatter name, and both
// the lookup and the answer use the latter.
//
// MUTATION -- compare against s.Item instead of s.Frontmatter.Name -- turns
// this red.
func TestUninvokedSkillMates_SpeaksTheEngineFacingName(t *testing.T) {
	delivered := []*LoadedSkill{
		deliveredSkill("ops", "admit-item", "admit", "ctxloom:link_id=nightly"),
		deliveredSkill("ops", "unattended-item", "unattended", "ctxloom:link_id=nightly"),
	}
	assert.Equal(t, []string{"unattended"}, UninvokedSkillMates(delivered, "admit", never))
	assert.Nil(t, UninvokedSkillMates(delivered, "admit-item", never), "the item key is not an invocable name")
}

// TestUninvokedSkillMates_GroupKeyIsTheBundle pins that two bundles sharing a
// link id form two groups (LinkGroups' contract): one author's skill never
// points at another author's.
//
// MUTATION -- drop the same-bundle check -- turns this red.
func TestUninvokedSkillMates_GroupKeyIsTheBundle(t *testing.T) {
	delivered := []*LoadedSkill{
		deliveredSkill("ops", "admit", "admit", "ctxloom:link_id=nightly"),
		deliveredSkill("other", "watch", "watch", "ctxloom:link_id=nightly"),
	}
	assert.Nil(t, UninvokedSkillMates(delivered, "admit", never))
}

// TestUninvokedSkillMates_UnknownSkillHasNoMates: a name that is not a
// delivered skill at all (a skill the engine found elsewhere) yields nothing.
func TestUninvokedSkillMates_UnknownSkillHasNoMates(t *testing.T) {
	delivered := []*LoadedSkill{
		deliveredSkill("ops", "admit", "admit", "ctxloom:link_id=nightly"),
		deliveredSkill("ops", "unattended", "unattended", "ctxloom:link_id=nightly"),
	}
	assert.Nil(t, UninvokedSkillMates(delivered, "stranger", never))
}
