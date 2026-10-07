package ident

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestItemKindNoun_IsTheUserFacingName: every core kind renders as the noun a
// user types and reads (`ctxloom command`, search_content's types), which is
// NOT its stored spelling for exactly one kind — a slash command is stored
// under KindPrompt so existing grants survive the item-kind rename.
func TestItemKindNoun_IsTheUserFacingName(t *testing.T) {
	core := map[ItemKind]string{
		KindFragment: "fragment",
		KindPrompt:   "command",
		KindMCP:      "mcp",
		KindHook:     "hook",
		KindSkill:    "skill",
	}
	for _, kind := range ItemKinds() {
		want, pinned := core[kind]
		if assert.Truef(t, pinned, "ItemKinds() declares %q with no pinned noun here", kind) {
			assert.Equalf(t, want, kind.Noun(), "core kind %q", kind)
		}
	}
	const registryDeclared ItemKind = "profiles"
	assert.Equal(t, "profiles", registryDeclared.Noun(), "a kind outside the closed core renders as itself")
}
