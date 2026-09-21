package mock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/engine/conformance"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// The missing-skills arm has a subject: NoSkills exports no skill while
// mock's Exports do. If a change makes NoSkills export one, THIS TEST IS THE
// ONE THAT SHOULD STOP YOU: the absence is the double's entire purpose.
func TestExports_NoSkillsDoubleExportsNoSkill(t *testing.T) {
	items := engine.Items{
		Commands: []engine.CommandItem{{Ref: "b#prompts/c", Name: "b/c", Body: []byte("body")}},
		Skills:   []engine.SkillItem{{Ref: "b#skills/s", Name: "s"}},
	}
	ex, err := New().Exports(items)
	require.NoError(t, err)
	require.Len(t, ex.Commands, 1)
	assert.True(t, ex.Commands[0].Enabled, "mock exports every command")
	require.Len(t, ex.Skills, 1)
	assert.True(t, ex.Skills[0].Enabled, "mock exports every skill")

	var noSkills engine.Engine
	for _, e := range Doubles() {
		if e.Root().Name == NameNoSkills {
			noSkills = e
		}
	}
	require.NotNil(t, noSkills, "the registry composes the no-skills double")
	ex, err = noSkills.Exports(items)
	require.NoError(t, err)
	assert.Len(t, ex.Commands, 1)
	assert.Empty(t, ex.Skills, "NoSkills exports no skill — it is the only subject the missing-surface arm has")
}

// TestBuild_EverySurfaceDefaultsToTheSessionHome pins the ruling on the
// mock: every static approach's first root is the session home; the project
// root is offered second, reached only by a binding's `roots:` selection.
func TestBuild_EverySurfaceDefaultsToTheSessionHome(t *testing.T) {
	for _, e := range Doubles() {
		for kind, a := range e.Root().Surfaces() {
			roots := a.Traits().Roots
			require.NotEmpty(t, roots, "%s kind %v declares no root", e.Root().Name, kind)
			assert.Equal(t, present.RootSessionHome, roots[0], "%s kind %v (%s): the first root is the session home", e.Root().Name, kind, a.Name())
			assert.True(t, a.Traits().Offers(present.RootProjectRoot), "%s kind %v (%s): the project root stays selectable", e.Root().Name, kind, a.Name())
		}
	}
}

// TestRoute_DefaultBindingPlansOnlySessionHomeRoots is the launch-capture
// form: the plan Resolve carries for a binding that selects no root targets
// the session home for every static kind.
func TestRoute_DefaultBindingPlansOnlySessionHomeRoots(t *testing.T) {
	plan, err := conformance.RouteFor(t, conformance.PackageFixture(t), New())
	require.NoError(t, err)
	require.NotEmpty(t, plan.Static)
	for _, it := range plan.Static {
		assert.Equal(t, present.RootSessionHome, it.Root, "kind %v routes through %s under %v", it.Kind, it.Approach, it.Root)
	}
}
