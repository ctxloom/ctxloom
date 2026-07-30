package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// harp's --format reader must agree with the family-wide cliemit.Resolve on
// every input. A private copy drifts silently: an empty --format has to render
// text, as it does for every other binary in the family, not fail as an
// unsupported format. This is the pin that harp never re-grows one.
func TestResolveFormat_MatchesSharedResolver(t *testing.T) {
	cases := []struct {
		name string
		// set reports whether --format is passed at all; raw is its value.
		set bool
		raw string
		// wrongType registers --format as an int flag instead of a string.
		// U004-F04: harp used to read it with `raw, _ := GetString(...)`, which
		// discards the only error the lookup can produce and degrades to
		// "unsupported format: \"\"" — a message describing a value the user
		// never typed. The shared resolver reports the wiring bug instead.
		wrongType bool
	}{
		{name: "unset", set: false},
		{name: "empty", set: true, raw: ""},
		{name: "text", set: true, raw: "text"},
		{name: "json", set: true, raw: "json"},
		{name: "yaml", set: true, raw: "yaml"},
		{name: "toml", set: true, raw: "toml"},
		{name: "markdown", set: true, raw: "markdown"},
		{name: "bogus", set: true, raw: "xml"},
		{name: "wrong type", wrongType: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newCmd := func() *cobra.Command {
				cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
				if tc.wrongType {
					cmd.Flags().Int("format", 0, "")
					return cmd
				}
				cmd.Flags().String("format", string(clifmt.FormatText), "")
				if tc.set {
					require.NoError(t, cmd.Flags().Set("format", tc.raw))
				}
				return cmd
			}

			gotFormat, gotErr := resolveFormat(newCmd())
			wantFormat, wantErr := cliemit.Resolve(newCmd())

			if wantErr != nil {
				require.Error(t, gotErr, "shared resolver errored; harp must too")
				assert.Equal(t, wantErr.Error(), gotErr.Error())
				return
			}
			require.NoError(t, gotErr)
			assert.Equal(t, wantFormat, gotFormat)
		})
	}
}
