package priority

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
)

// resolveTagValues keys three different kinds of entry into ONE flat
// namespace: the bare target, "target=value", and "target=*". That is only
// unambiguous while a target cannot itself contain '=' — and tagma's quoting
// extension lets it: `ns:"impact=3"=7` parses cleanly, with Key "impact=3".
// Its bare-target entry would then land on exactly the slot `ns:impact=3`
// occupies as another tag's composite presence key, so a
// `{{ns:impact=3}}` presence test — which every formula reads as 1.0 or
// absent — would instead read that other tag's arbitrary numeric value.
//
// A target carrying '=' is un-referenceable by construction (any placeholder
// naming it reads as the composite form), so it must contribute nothing
// rather than corrupt the slot that IS referenceable.
func TestResolveTagValues_TargetContainingEqualsCannotShadowACompositeKey(t *testing.T) {
	// The crafted tag ALONE. The task carries no `ns:impact=3` at all, so
	// that composite key must be absent; instead it used to be present and
	// carrying the crafted tag's own numeric value.
	values := resolveTagValues([]string{`ns:"impact=3"=7`})

	if v, present := values["ns:impact=3"]; present {
		t.Errorf("composite key ns:impact=3 is set to %v by a task that carries no such tag", v)
	}
	for k := range values {
		assert.NotContains(t, k, "impact=3", "the crafted target must contribute no entries at all")
	}

	// And a well-formed tag on the same target still behaves exactly as
	// before: bare numeric entry, composite presence key, universal key.
	clean := resolveTagValues([]string{"ns:impact=3"})
	assert.Equal(t, 3.0, clean["ns:impact"])
	assert.Equal(t, 1.0, clean["ns:impact=3"])
	assert.Equal(t, 1.0, clean["ns:impact=*"])
}

// The same collision, driven through Compute so the consequence is visible
// where it matters: a `{{triage:impact=3}}` presence test scored a task
// carrying NO triage:impact tag at all — at whatever number the crafted
// tag's value happened to be (99 here, against a formula whose every honest
// answer is 0 or 1).
func TestCompute_CraftedTargetDoesNotMovePriority(t *testing.T) {
	schema := mustSchema(t,
		`tagma.priority_fn:"triage:kind"="{{triage:impact=3}}"`,
	)
	untagged := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, CreatedAt: fixedNow}}
	crafted := []tasks.Task{{HarpID: "a", Status: tasks.StatusToDo, Tags: []string{`triage:"impact=3"=99`}, CreatedAt: fixedNow}}

	untaggedRes, _, err := Compute(untagged, schema, fixedNow)
	require.NoError(t, err)
	craftedRes, _, err := Compute(crafted, schema, fixedNow)
	require.NoError(t, err)

	assert.Equal(t, untaggedRes["a"].Raw, craftedRes["a"].Raw,
		"a tag whose target carries '=' must not satisfy another target's presence test")
}
