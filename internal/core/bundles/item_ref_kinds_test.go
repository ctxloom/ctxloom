// This file is an EXTERNAL test package on purpose.
//
// internal/core/bundles hard-codes the item-ref kind directory segments
// ("fragments", "prompts", "skills") at its gate calls, while
// internal/core/ident.ItemKind.Dir() is the declared authority for that grammar.
// The two are not wired together and nothing detects a divergence — a rename on
// either side silently changes the item address the loader emits, so a
// selector written against the declared grammar stops matching.
//
// Sourcing the literals FROM internal/core/ident would add a production import edge
// out of this package into the identity package, whose vocabulary is
// deliberately closed. So the coupling is not removed here; it is made LOUD. `package
// bundles_test` keeps this an XTest import, which adds no production edge, and
// the assertions below fail the moment either side moves.
package bundles_test

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// TestItemRefKindDirs_MatchTheIdentAuthority pins the ref segments the loader
// actually emits against ident.ItemKind.Dir(). It reads real content out of a
// real bundle rather than comparing constants, so it covers the literals at the
// call sites — the thing that can drift — not merely the constants they equal.
//
// It asserts on the READ's own ItemRef rather than on what an authorizer was handed.
// Exposure carries a parsed ident.Ref, and parsing is exactly what would hide
// the drift this test exists to catch: ident.ParseSelector maps BOTH "commands"
// and "prompts" onto KindPrompt, so a loader that emitted the wrong segment
// would still arrive at the right Kind and the assertion would pass while the
// emitted string was wrong.
func TestItemRefKindDirs_MatchTheIdentAuthority(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	root := paths.BundlesLayoutRoot(bundlesDir, paths.LayoutV2)
	bundleDir := filepath.Join(root, "kit")

	bundletree.Write(t, fsys, root, "kit", "version: \"1.0\"\n"+
		"fragments:\n  frag:\n    content: f\n"+
		"commands:\n  cmd:\n    content: c\n")
	testsupport.WriteFile(t, fsys, bundleDir+"/skills/sk/SKILL.md",
		[]byte("---\nname: sk\ndescription: Does a thing well.\n---\n\nbody\n"), 0644)

	l := bundles.NewLoader(bundles.NewProjectReader(fsys, []string{bundlesDir}))

	fragReads, err := l.ReadFragment("kit#fragments/frag")
	require.NoError(t, err)
	require.Len(t, fragReads, 1)
	cmdReads, err := l.ReadCommand("kit#commands/cmd")
	require.NoError(t, err)
	require.Len(t, cmdReads, 1)
	skillReads, err := l.Catalog().ReadSkill("kit#skills/sk")
	require.NoError(t, err)
	require.Len(t, skillReads, 1)

	seen := []string{fragReads[0].ItemRef, cmdReads[0].ItemRef, skillReads[0].ItemRef}

	// The prompt segment is deliberately "prompts", not "commands": the
	// item-kind rename must not invalidate existing grants. That decision lives
	// in ident.KindPrompt.Dir(), and this is what keeps the loader agreeing
	// with it.
	assert.Contains(t, seen[0], "#"+ident.KindFragment.Dir()+"/frag")
	assert.Contains(t, seen[1], "#"+ident.KindPrompt.Dir()+"/cmd")
	assert.Contains(t, seen[2], "#"+ident.KindSkill.Dir()+"/sk")

	// Named explicitly too, so a rename on BOTH sides at once — which would keep
	// the assertions above green while re-keying every recorded grant — still
	// fails here.
	assert.Equal(t, "fragments", ident.KindFragment.Dir())
	assert.Equal(t, "prompts", ident.KindPrompt.Dir())
	assert.Equal(t, "skills", ident.KindSkill.Dir())
	assert.Equal(t, "mcp", ident.KindMCP.Dir())
	assert.Equal(t, "hooks", ident.KindHook.Dir())
}
