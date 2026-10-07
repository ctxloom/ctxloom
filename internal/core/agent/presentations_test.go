package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The names are the PERSISTED USER VOCABULARY: users type them and
// agent-binding files on disk hold them, so they are spelled here exactly as
// they are spelled on disk. A FAKE table stands in for a real engine's, so a
// change to claude's declaration does not turn this seam's tests red.
const (
	fakeUnsafeFileName   = "unsafe-file"
	fakeSystemPromptName = "system-prompt"
	fakeHookName         = "hook"
)

func fakeContextPresentations() Presentations {
	return Presents(fakeUnsafeFileName, fakeSystemPromptName, fakeHookName)
}

// TestPresentations_NamesAreSortedAndCarryTheDefault: Names is every declared
// name, sorted — order carries no meaning, the default is a named field — and
// the default is always one of them.
func TestPresentations_NamesAreSortedAndCarryTheDefault(t *testing.T) {
	p := Presents(fakeSystemPromptName, fakeUnsafeFileName, fakeHookName)
	assert.Equal(t, []string{fakeHookName, fakeSystemPromptName, fakeUnsafeFileName}, p.Names())
	assert.Equal(t, fakeSystemPromptName, p.Default(), "the default is the named first argument, not the first sorted name")
	assert.Equal(t, []string{fakeUnsafeFileName}, Presents(fakeUnsafeFileName).Names(), "a default alone is a complete declaration")
}

// TestPresentations_ADefaultRepeatedAmongTheOthersIsDeclaredOnce: a table
// that also lists its default among the alternatives declares each name once,
// so a completion list never offers a name twice.
func TestPresentations_ADefaultRepeatedAmongTheOthersIsDeclaredOnce(t *testing.T) {
	p := Presents(fakeUnsafeFileName, fakeHookName, fakeUnsafeFileName)
	assert.Equal(t, []string{fakeHookName, fakeUnsafeFileName}, p.Names())
}

// TestPresentations_NamesIsTheCallersOwn: a caller that edits the slice Names
// returned does not edit the table — declarations are built once and then read
// concurrently by everything that validates a binding.
func TestPresentations_NamesIsTheCallersOwn(t *testing.T) {
	p := fakeContextPresentations()
	got := p.Names()
	got[0] = "tampered"
	assert.NotContains(t, p.Names(), "tampered")
}

// TestDeclaration_AnAbsentKindHasNoNamesAndNoDefault: a kind the engine does
// not declare is a FOLD — no names, no default — never a zero-value default
// that would read as "selectable".
func TestDeclaration_AnAbsentKindHasNoNamesAndNoDefault(t *testing.T) {
	d := Declaration{SurfaceContext: fakeContextPresentations()}
	assert.Nil(t, d.Names(SurfaceMCP))
	_, ok := d.Default(SurfaceMCP)
	assert.False(t, ok)

	def, ok := d.Default(SurfaceContext)
	assert.True(t, ok)
	assert.Equal(t, fakeUnsafeFileName, def)
}

// TestApproachNames_IsTheUnionAcrossKindsAndDeclarations: what a CLI can offer
// as "names that exist at all" before an engine is chosen.
func TestApproachNames_IsTheUnionAcrossKindsAndDeclarations(t *testing.T) {
	a := Declaration{SurfaceContext: Presents(fakeUnsafeFileName, fakeSystemPromptName)}
	b := Declaration{SurfaceMCP: Presents("mcp-config", fakeUnsafeFileName)}
	assert.Equal(t, []string{"mcp-config", fakeSystemPromptName, fakeUnsafeFileName}, ApproachNames(a, b))
	assert.Equal(t, []string{fakeSystemPromptName, fakeUnsafeFileName}, a.AllNames())
}
