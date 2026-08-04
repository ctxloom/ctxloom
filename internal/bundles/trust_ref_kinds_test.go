// This file is an EXTERNAL test package on purpose.
//
// internal/bundles hard-codes the trust-ref kind directory segments
// ("fragments", "prompts", "skills") at its gate calls, while
// internal/trust.ItemKind.Dir() is the declared authority for that grammar.
// The two are not wired together and nothing detects a divergence — a rename on
// either side silently re-keys every trust grant, because a grant is looked up
// by the ref string.
//
// Sourcing the literals FROM internal/trust would add a production import edge
// out of this package into the trust model, whose vocabulary is deliberately
// frozen. So the coupling is not removed here; it is made LOUD. `package
// bundles_test` keeps this an XTest import, which adds no production edge, and
// the assertions below fail the moment either side moves.
package bundles_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestTrustRefKindDirs_MatchTheTrustAuthority pins the ref segments the loader
// actually emits against trust.ItemKind.Dir(). It drives real content through
// the gate rather than comparing constants, so it covers the literals at the
// call sites — the thing that can drift — not merely the constants they equal.
func TestTrustRefKindDirs_MatchTheTrustAuthority(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	bundleDir := bundlesDir + "/kit"

	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/bundle.yaml", []byte(
		"version: \"1.0\"\n"+
			"fragments:\n  frag:\n    content: f\n"+
			"commands:\n  cmd:\n    content: c\n"+
			"skills:\n  sk: {}\n"), 0644))
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/skills/sk/SKILL.md",
		[]byte("---\nname: sk\ndescription: Does a thing well.\n---\n\nbody\n"), 0644))

	var seen []string
	gate := func(ref string, _ []byte, _, _ string) bool {
		seen = append(seen, ref)
		return true
	}
	l := bundles.NewPipeline(
		bundles.NewLoader(bundles.NewProjectReader(fsys, []string{bundlesDir})), gate, false)

	_, err := l.GetFragment("kit#fragments/frag")
	require.NoError(t, err)
	_, err = l.GetCommand("kit#commands/cmd")
	require.NoError(t, err)
	_, err = l.GetSkill("kit#skills/sk")
	require.NoError(t, err)

	require.Len(t, seen, 3)

	// The prompt segment is deliberately "prompts", not "commands": the
	// item-kind rename must not invalidate existing grants. That decision lives
	// in trust.KindPrompt.Dir(), and this is what keeps the loader agreeing
	// with it.
	assert.Contains(t, seen[0], "#"+trust.KindFragment.Dir()+"/frag")
	assert.Contains(t, seen[1], "#"+trust.KindPrompt.Dir()+"/cmd")
	assert.Contains(t, seen[2], "#"+trust.KindSkill.Dir()+"/sk")

	// Named explicitly too, so a rename on BOTH sides at once — which would keep
	// the assertions above green while re-keying every recorded grant — still
	// fails here.
	assert.Equal(t, "fragments", trust.KindFragment.Dir())
	assert.Equal(t, "prompts", trust.KindPrompt.Dir())
	assert.Equal(t, "skills", trust.KindSkill.Dir())
	assert.Equal(t, "mcp", trust.KindMCP.Dir())
	assert.Equal(t, "hooks", trust.KindHook.Dir())
}
