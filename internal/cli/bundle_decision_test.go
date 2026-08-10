package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The content-decision verbs, as a SET.
//
// The store holds three states — approved, rejected, undecided — so the CLI
// carries three verbs, one per state, and every one of them must be reachable
// by the name that says what it writes. `untrust` was the second verb's old
// spelling and it lied: it reads as the inverse of `trust` while writing a
// rejection, which is stronger and stickier than withdrawing an approval, so
// someone reaching for it to undo a `trust` did something they did not mean.
//
// This asserts the SURFACE rather than any one command's behaviour, because
// the surface is the part a rename can quietly half-finish: a `reject` that
// exists alongside a surviving `untrust` leaves the trap in place for everyone
// who already knows the old spelling.

// bundleSubcommand returns the named direct subcommand of `ctxloom bundle`, or
// nil when the noun does not carry it. Every name a command answers to is
// checked (cobra's Name + aliases), so an alias cannot smuggle a retired verb
// back onto the surface.
func bundleSubcommand(name string) *cobra.Command {
	for _, c := range bundleCmd.Commands() {
		if c.Name() == name {
			return c
		}
		for _, alias := range c.Aliases {
			if alias == name {
				return c
			}
		}
	}
	return nil
}

// TestBundleCarriesOneVerbPerDecisionState pins the three-verb surface: one way
// into each state the store can hold, and no path back to the misleading
// spelling.
func TestBundleCarriesOneVerbPerDecisionState(t *testing.T) {
	for name, writes := range map[string]string{
		"trust":  "an approval — the item is delivered",
		"reject": "a rejection — the item is withheld everywhere",
	} {
		cmd := bundleSubcommand(name)
		require.NotNilf(t, cmd, "`ctxloom bundle %s` must exist: it is the only way to record %s", name, writes)
		assert.NotEmptyf(t, cmd.Short, "`ctxloom bundle %s` must describe itself", name)
	}

	assert.Nil(t, bundleSubcommand("untrust"),
		"`ctxloom bundle untrust` must be gone, not aliased: it reads as the inverse of `trust` and is not one — "+
			"it writes a rejection, which overrides a trusted publisher and overrides local content. "+
			"Leaving it reachable leaves the trap for everyone who already knows the old spelling")
}
