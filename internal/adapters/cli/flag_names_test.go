package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The report-then-apply commands share one confirming flag, --yes; a file a
// command writes is named with --output. The superseded spellings are gone,
// not aliased.
func TestFlagNames_ApplyIsYesAndOutIsOutput(t *testing.T) {
	for _, c := range []struct {
		path      string
		want, old string
		shorthand string
	}{
		{"container prune", yesFlagName, "apply", "y"},
		{"session adopt", yesFlagName, "apply", "y"},
		{"skill export", outputFlagName, "out", "o"},
	} {
		cmd, _, err := rootCmd.Find(strings.Fields(c.path))
		require.NoError(t, err, c.path)
		f := cmd.Flags().Lookup(c.want)
		require.NotNilf(t, f, "%s must carry --%s", c.path, c.want)
		assert.Equal(t, c.shorthand, f.Shorthand, c.path)
		assert.Nilf(t, cmd.Flags().Lookup(c.old), "%s must not carry --%s", c.path, c.old)
	}
}
