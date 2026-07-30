package cli

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPushAlias_ExposesTheIdenticalFlagSet pins what an ALIAS means: the
// deprecated `command push` and the canonical `bundle push` run the same
// publish (both call pushBundle), so they must also OFFER the same thing — same
// flag names, same shorthands, same defaults, same help. A flag whose help text
// or default differs between the two documents two different commands for one
// behaviour, and a flag present on only one silently drops from the alias.
func TestPushAlias_ExposesTheIdenticalFlagSet(t *testing.T) {
	describe := func(fs *pflag.FlagSet) map[string][3]string {
		got := map[string][3]string{}
		fs.VisitAll(func(f *pflag.Flag) {
			got[f.Name] = [3]string{f.Shorthand, f.DefValue, f.Usage}
		})
		return got
	}

	canonical := describe(bundlePushCmd.Flags())
	alias := describe(commandPushCmd.Flags())

	require.NotEmpty(t, canonical, "bundle push must register its publish flags")
	assert.Equal(t, canonical, alias,
		"`command push` must expose byte-identical flags to `bundle push` — it is an alias for it")
}
