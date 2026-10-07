package bundles

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// TestItemRefFor_GoldenAgainstDeletedStringRoute pins the strings a
// "<source>#<kind>/<item>" item ref renders as, for a builtin, companion,
// local and git bundle. They MUST NOT MOVE: profiles, selectors and the
// lockfile address items by them, and moving one silently stops every
// reference written against the old spelling from matching.
//
// The "want" literals were captured by running the string route these refs
// were once derived through, not hand-derived, and are pinned as literals
// rather than recomputed: a second copy of that logic here would be a clone
// of managedhooks.parseSourceRef (the one production caller that still needs
// the conversion). Literal expectations make this a golden test in the
// ordinary sense: no shared logic to keep in sync.
func TestItemRefFor_GoldenAgainstDeletedStringRoute(t *testing.T) {
	companionRef, err := ident.CompanionRef("ltk")
	require.NoError(t, err)
	localRef, err := ident.LocalRef("my-tools")
	require.NoError(t, err)
	gitRef, err := ident.GitRef("github.com", "/acme/repo", "tooling")
	require.NoError(t, err)

	cases := []struct {
		name string
		src  ident.BundleRef
		want map[ident.ItemKind]string
	}{
		{
			name: "companion",
			src:  companionRef,
			want: map[ident.ItemKind]string{
				ident.KindFragment: "ctxloom+companion:ltk#fragments/x",
				ident.KindPrompt:   "ctxloom+companion:ltk#prompts/x",
				ident.KindMCP:      "ctxloom+companion:ltk#mcp/x",
				ident.KindHook:     "ctxloom+companion:ltk#hooks/PreToolUse/0",
				ident.KindSkill:    "ctxloom+companion:ltk#skills/x",
			},
		},
		{
			name: "local",
			src:  localRef,
			want: map[ident.ItemKind]string{
				ident.KindFragment: "ctxloom+local:my-tools#fragments/x",
				ident.KindPrompt:   "ctxloom+local:my-tools#prompts/x",
				ident.KindMCP:      "ctxloom+local:my-tools#mcp/x",
				ident.KindHook:     "ctxloom+local:my-tools#hooks/PreToolUse/0",
				ident.KindSkill:    "ctxloom+local:my-tools#skills/x",
			},
		},
		{
			// The exact worked example from the U3b-3 design pack's own CLI
			// grammar table (§2): the retired spelling for a pinned remote
			// bundle ("https://github.com/acme/repo@bundles/tooling") and its
			// canonical successor.
			name: "git",
			src:  gitRef,
			want: map[ident.ItemKind]string{
				ident.KindFragment: "ctxloom+git://github.com/acme/repo//bundles/tooling#fragments/x",
				ident.KindPrompt:   "ctxloom+git://github.com/acme/repo//bundles/tooling#prompts/x",
				ident.KindMCP:      "ctxloom+git://github.com/acme/repo//bundles/tooling#mcp/x",
				ident.KindHook:     "ctxloom+git://github.com/acme/repo//bundles/tooling#hooks/PreToolUse/0",
				ident.KindSkill:    "ctxloom+git://github.com/acme/repo//bundles/tooling#skills/x",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for kind, want := range tc.want {
				item := "x"
				if kind == ident.KindHook {
					item = "PreToolUse/0"
				}
				got, err := ItemRefFor(tc.src, kind, item)
				require.NoError(t, err)
				require.Equal(t, want, got,
					"%s/%s: S2 must not move the minted item-ref string", tc.name, kind)
			}
		})
	}
}
