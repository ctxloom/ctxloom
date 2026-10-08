package ident

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestItemKindDir_RegistryDeclaredKindsPassThrough pins that ItemKind.Dir()'s
// default arm is LOAD-BEARING, not an unchecked fallthrough.
//
// ItemKinds() documents the vocabulary as CLOSED at this package's core and
// OPEN at the surface-type registry: a kind may be declared outside this
// package, and internal/adapters/content.KindProfile ("profiles") is a live instance —
// its surface type names its own directory as KindProfile.Dir(). Removing the
// passthrough, so an unregistered kind resolved to an error or an empty
// segment, would take the registry's extension point with it.
//
// The passthrough is safe rather than lax because Dir() only ADDRESSES: it
// names where an item of the kind lives, and decides nothing else. Entry from
// a runtime string into the closed core stays closed (ParseItemKind refuses a
// spelling outside ItemKinds). Addressing is open; parsing is closed.
func TestItemKindDir_RegistryDeclaredKindsPassThrough(t *testing.T) {
	// The closed core, each with its own directory segment.
	core := map[ItemKind]string{
		KindFragment: "fragments",
		KindPrompt:   "prompts",
		KindMCP:      "mcp",
		KindHook:     "hooks",
		KindSkill:    "skills",
	}
	for kind, dir := range core {
		assert.Equalf(t, dir, kind.Dir(), "core kind %q", kind)
	}
	for _, kind := range ItemKinds() {
		assert.Containsf(t, core, kind, "ItemKinds() declares %q with no pinned directory here", kind)
	}

	// Declared elsewhere: internal/adapters/content.KindProfile. Spelled as a literal
	// because package content imports this one.
	const registryDeclared ItemKind = "profiles"
	assert.NotContains(t, ItemKinds(), registryDeclared,
		"the profile kind is deliberately outside the closed core")
	assert.Equal(t, "profiles", registryDeclared.Dir(),
		"a registry-declared kind must address through Dir() unchanged — content's profile surface names its directory this way")
}
