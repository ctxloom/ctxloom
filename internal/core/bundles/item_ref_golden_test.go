package bundles

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// TestItemRefFor_GoldenAgainstDeletedStringRoute is the S2 proof obligation:
// deleting Bundle.sourceRef's string half, BundleRead.TrustSourceRef,
// ident.ItemRefFromSource and ident.BundleRefFromSource is a ROUTE deletion,
// never a KEY migration. The strings a "<source>#<kind>/<item>" gate ref
// renders as MUST NOT MOVE — they are trust-store keys, and moving one
// silently invalidates every grant recorded against it.
//
// The "want" literals were captured by RUNNING fd8b729e's actual deleted
// route (ident.ItemRefFromSource -> ident.BundleRefFromSource ->
// ident.ParseItemRef -> Ref.AsBundleRef, fed the pre-canonical source strings
// a builtin/companion/local/git bundle carried before this slice) — not
// hand-derived. They are pinned as literals here rather than recomputed by a
// second copy of that deleted logic living in this test file: a prior draft
// duplicated ident.BundleRefFromSource's body locally for exactly this
// comparison, and the reprise duplication gate correctly flagged it as an
// exact-normalized clone of managedhooks.parseSourceRef (the one production
// caller that still needs that conversion, documented there). Literal
// expectations make this a golden test in the ordinary sense: no shared logic
// to keep in sync, just the string a grant is keyed on.
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
