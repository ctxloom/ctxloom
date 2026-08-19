package bundles

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestItemRefFor_GoldenAgainstDeletedStringRoute is the S2 proof obligation:
// deleting Bundle.sourceRef's string half, BundleRead.TrustSourceRef,
// trust.ItemRefFromSource and trust.BundleRefFromSource is a ROUTE deletion,
// never a KEY migration. The strings a "<source>#<kind>/<item>" gate ref
// renders as MUST NOT MOVE — they are trust-store keys, and moving one
// silently invalidates every grant recorded against it.
//
// The "want" literals were captured by RUNNING fd8b729e's actual deleted
// route (trust.ItemRefFromSource -> trust.BundleRefFromSource ->
// trust.ParseItemRef -> Ref.AsBundleRef, fed the pre-canonical source strings
// a builtin/companion/local/git bundle carried before this slice) — not
// hand-derived. They are pinned as literals here rather than recomputed by a
// second copy of that deleted logic living in this test file: a prior draft
// duplicated trust.BundleRefFromSource's body locally for exactly this
// comparison, and the reprise duplication gate correctly flagged it as an
// exact-normalized clone of lm/backends.parseSourceRef (the one production
// caller that still needs that conversion, documented there). Literal
// expectations make this a golden test in the ordinary sense: no shared logic
// to keep in sync, just the string a grant is keyed on.
func TestItemRefFor_GoldenAgainstDeletedStringRoute(t *testing.T) {
	builtinRef, err := trust.BuiltinRef("ltk")
	require.NoError(t, err)
	companionRef, err := trust.CompanionRef("ltk")
	require.NoError(t, err)
	localRef, err := trust.LocalRef("my-tools")
	require.NoError(t, err)
	gitRef, err := trust.GitRef("github.com", "/acme/repo", "tooling")
	require.NoError(t, err)

	cases := []struct {
		name string
		src  trust.BundleRef
		want map[trust.ItemKind]string
	}{
		{
			name: "builtin",
			src:  builtinRef,
			want: map[trust.ItemKind]string{
				trust.KindFragment: "ctxloom+builtin:ltk#fragments/x",
				trust.KindPrompt:   "ctxloom+builtin:ltk#prompts/x",
				trust.KindMCP:      "ctxloom+builtin:ltk#mcp/x",
				trust.KindHook:     "ctxloom+builtin:ltk#hooks/PreToolUse/0",
				trust.KindSkill:    "ctxloom+builtin:ltk#skills/x",
			},
		},
		{
			name: "companion",
			src:  companionRef,
			want: map[trust.ItemKind]string{
				trust.KindFragment: "ctxloom+companion:ltk#fragments/x",
				trust.KindPrompt:   "ctxloom+companion:ltk#prompts/x",
				trust.KindMCP:      "ctxloom+companion:ltk#mcp/x",
				trust.KindHook:     "ctxloom+companion:ltk#hooks/PreToolUse/0",
				trust.KindSkill:    "ctxloom+companion:ltk#skills/x",
			},
		},
		{
			name: "local",
			src:  localRef,
			want: map[trust.ItemKind]string{
				trust.KindFragment: "ctxloom+local:my-tools#fragments/x",
				trust.KindPrompt:   "ctxloom+local:my-tools#prompts/x",
				trust.KindMCP:      "ctxloom+local:my-tools#mcp/x",
				trust.KindHook:     "ctxloom+local:my-tools#hooks/PreToolUse/0",
				trust.KindSkill:    "ctxloom+local:my-tools#skills/x",
			},
		},
		{
			// The exact worked example from the U3b-3 design pack's own CLI
			// grammar table (§2): the retired spelling for a pinned remote
			// bundle ("https://github.com/acme/repo@bundles/tooling") and its
			// canonical successor.
			name: "git",
			src:  gitRef,
			want: map[trust.ItemKind]string{
				trust.KindFragment: "ctxloom+git://github.com/acme/repo//bundles/tooling#fragments/x",
				trust.KindPrompt:   "ctxloom+git://github.com/acme/repo//bundles/tooling#prompts/x",
				trust.KindMCP:      "ctxloom+git://github.com/acme/repo//bundles/tooling#mcp/x",
				trust.KindHook:     "ctxloom+git://github.com/acme/repo//bundles/tooling#hooks/PreToolUse/0",
				trust.KindSkill:    "ctxloom+git://github.com/acme/repo//bundles/tooling#skills/x",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for kind, want := range tc.want {
				item := "x"
				if kind == trust.KindHook {
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
