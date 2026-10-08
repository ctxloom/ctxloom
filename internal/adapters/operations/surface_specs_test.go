package operations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestSplitSurfaceSpecs_SplitsKindMechanismAndDest: KIND, then the mechanism
// after `=`, then the destination after the first `:` following it, so a
// Windows drive-letter destination survives; each part is trimmed.
func TestSplitSurfaceSpecs_SplitsKindMechanismAndDest(t *testing.T) {
	got, err := SplitSurfaceSpecs([]string{" context = file : C:\\docs\\AGENTS.md ", "mcp", "skills=file"})
	require.NoError(t, err)
	require.Equal(t, []RawSurfaceSpec{
		{Kind: "context", Mechanism: "file", Dest: `C:\docs\AGENTS.md`},
		{Kind: "mcp"},
		{Kind: "skills", Mechanism: "file"},
	}, got)
}

// TestSplitSurfaceSpecs_ARepeatIsOneSurface: the same kind named the same
// way twice is one surface, and the specs after the repeat are still read.
func TestSplitSurfaceSpecs_ARepeatIsOneSurface(t *testing.T) {
	got, err := SplitSurfaceSpecs([]string{"context=file:a.md", "context=file:a.md", "hooks"})
	require.NoError(t, err)
	require.Equal(t, []RawSurfaceSpec{{Kind: "context", Mechanism: "file", Dest: "a.md"}, {Kind: "hooks"}}, got)
}

// TestSplitSurfaceSpecs_OneKindTwoWaysIsRefused: a kind named two different
// ways is refused, quoting both spellings as typed after the kind.
func TestSplitSurfaceSpecs_OneKindTwoWaysIsRefused(t *testing.T) {
	_, err := SplitSurfaceSpecs([]string{"context=file:a.md", "context=hook"})
	require.EqualError(t, err, "--surface names context twice, as file:a.md and hook; a surface is delivered one way")
}

// TestParseSurfaceSpecs_ReadsKindsFromTheVocabulary: each kind is a
// surface kind; an unknown one is refused by name as a request error, and
// the splitter's refusal passes through.
func TestParseSurfaceSpecs_ReadsKindsFromTheVocabulary(t *testing.T) {
	got, err := ParseSurfaceSpecs([]string{"context=file:docs/AGENTS.md", "skills"})
	require.NoError(t, err)
	require.Equal(t, []SurfaceSpec{{Kind: present.Context, Mechanism: "file", Dest: "docs/AGENTS.md"}, {Kind: present.Skills}}, got)

	_, err = ParseSurfaceSpecs([]string{"contxt"})
	require.ErrorIs(t, err, ErrMaterializeRequest)
	require.ErrorContains(t, err, `"contxt" is not a surface kind`)

	_, err = ParseSurfaceSpecs([]string{"mcp", "mcp=file:x"})
	require.ErrorContains(t, err, "--surface names mcp twice")
}
