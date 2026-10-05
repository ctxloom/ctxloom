package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
)

// helpJargon is the internal vocabulary user-facing help does not use: a
// session is named by its session name, and compaction produces its summary.
// Flag names, JSON keys and MCP fields keep their spellings; this reads only
// the prose a user is shown.
var helpJargon = regexp.MustCompile(`(?i)\b(harps?|essences?)\b`)

// helpFilenames are on-disk names help may cite verbatim.
var helpFilenames = strings.NewReplacer("essence.md", "")

func TestHelp_SaysSessionNameAndSummary(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		if c.Hidden {
			return
		}
		texts := map[string]string{"use": c.Use, "short": c.Short, "long": c.Long, "example": c.Example}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			texts["--"+f.Name] = f.Usage
		})
		for field, text := range texts {
			if m := helpJargon.FindString(helpFilenames.Replace(text)); m != "" {
				assert.Failf(t, "help jargon", "%q %s says %q", c.CommandPath(), field, m)
			}
		}
	})
}
