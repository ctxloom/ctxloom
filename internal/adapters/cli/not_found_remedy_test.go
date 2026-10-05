package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestShowMiss_NamesTheListingCommand: a show on a name that does not exist
// ends with the one command that lists the names that do.
func TestShowMiss_NamesTheListingCommand(t *testing.T) {
	runCLIFixture(t)
	cases := []struct {
		args []string
		fix  string
	}{
		{[]string{"bundle", "show", "nosuch"}, bundleListFix},
		{[]string{"session", "show", "nosuch-session-name"}, sessionListFix},
	}
	for _, tc := range cases {
		err := runCLI(t, tc.args...).err
		require.Error(t, err, "%v", tc.args)
		fix, ok := clifmt.RemedyOf(err)
		require.True(t, ok, "%v: the miss names its fix: %v", tc.args, err)
		assert.Equal(t, tc.fix, fix, "%v", tc.args)
	}
}
