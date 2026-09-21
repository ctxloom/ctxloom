package composite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithLead_AppendsCallerBlocksAfterTheAssembledContext: a launch's
// caller-composed blocks — a resumed transcript, this launch's startup
// findings — ride the package's context after the assembly, in order, each
// a named fragment of the package, and the context's hash follows the text.
func TestWithLead_AppendsCallerBlocksAfterTheAssembledContext(t *testing.T) {
	pkg := Package{Context: Context{Text: "RULES", Hash: digest([]byte("RULES"))}}
	out := pkg.WithLead(Fragment{Name: "resumed-transcript", Body: "EARLIER"}, Fragment{Name: "findings", Body: "WARN"})
	require.Equal(t, strings.Join([]string{"RULES", "EARLIER", "WARN"}, contextSectionSeparator), out.Context.Text)
	assert.Equal(t, digest([]byte(out.Context.Text)), out.Context.Hash, "the hash follows the text")
	require.Len(t, out.Fragments, 2)
	assert.Equal(t, "resumed-transcript", out.Fragments[0].Value.Name)
	assert.Equal(t, "RULES", pkg.Context.Text, "the receiver is untouched")
}

// TestWithLead_ABlankBlockAddsNothing: an empty block neither separates nor
// names a fragment; a context-free package with one block IS that block.
func TestWithLead_ABlankBlockAddsNothing(t *testing.T) {
	out := Package{}.WithLead(Fragment{Name: "empty"}, Fragment{Name: "findings", Body: "WARN"})
	assert.Equal(t, "WARN", out.Context.Text)
	require.Len(t, out.Fragments, 1)
	assert.Equal(t, Package{}.Context, Package{}.WithLead().Context)
}
