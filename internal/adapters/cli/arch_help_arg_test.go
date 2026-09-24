//go:build arch

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// TestArch_HelpArgName_ReachedOnlyThroughHelpFallback holds the "help"
// shortcut to one definition. The idiom has twice been hand-copied into a
// command's RunE — once as eleven verbatim guards, and again, after they were
// collapsed, as seven inline fallbacks — and each copy is a site the next
// change to what the shortcut means has to find by hand. The parity pin in
// help_arg_test.go checks what each command DOES; only this sees how.
func TestArch_HelpArgName_ReachedOnlyThroughHelpFallback(t *testing.T) {
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	got := sourcedir.ReferencingFiles(t, dir, "helpArgName", false)
	assert.Equal(t, []string{"help_arg.go"}, got,
		"helpArgName belongs to helpFallback. A command that wants the \"help\" shortcut "+
			"calls helpFallback(cmd, name) at its not-found branch instead of comparing the name itself.")
}
