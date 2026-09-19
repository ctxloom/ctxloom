package trust

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestItemKindDir_RegistryDeclaredKindsPassThrough pins that ItemKind.Dir()'s
// default arm is LOAD-BEARING, not an unchecked fallthrough.
//
// ItemKinds() documents the vocabulary as CLOSED at this package's core and
// OPEN at the surface-type registry: a kind may be declared outside package
// trust, and internal/content.KindProfile ("profiles") is a live instance —
// its surface type names its own directory as KindProfile.Dir(). Removing the
// passthrough, so an unregistered kind resolved to an error or an empty
// segment, would take the registry's extension point with it.
//
// The reason that is safe rather than lax is that Dir() only ADDRESSES. A kind
// this binary's countersignature derivation does not know is inert: it can be
// neither approved nor rejected by content, which is what
// operations' TestAttestationFormFor_AnUnregisteredKindIsInert pins for the
// same set of kinds, empty string included. Addressing is open; attestation is
// closed.
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

	// Declared elsewhere: internal/content.KindProfile. Spelled as a literal
	// because package content imports this one.
	const registryDeclared ItemKind = "profiles"
	assert.NotContains(t, ItemKinds(), registryDeclared,
		"the profile kind is deliberately outside the closed core")
	assert.Equal(t, "profiles", registryDeclared.Dir(),
		"a registry-declared kind must address through Dir() unchanged — content's profile surface names its directory this way")
}
