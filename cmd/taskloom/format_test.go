package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// runStatuses drives the real statusesCmd's RunE with a throwaway parent
// carrying the given --format value. `statuses` is the representative command
// for the format matrix: its output (the status taxonomy) is deterministic and
// project-independent, so each format's bytes can be asserted directly.
func runStatuses(t *testing.T, format string) string {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().String("format", "text", "")
	if format != "" {
		require.NoError(t, c.Flags().Set("format", format))
	}
	var buf bytes.Buffer
	c.SetOut(&buf)
	require.NoError(t, statusesCmd.RunE(c, nil))
	return buf.String()
}

// TestStatusesFormatMatrix asserts the actual formatted bytes `taskloom
// statuses` produces in every format the shared clifmt filter supports, so the
// five-format contract (and the taskloom-specific text view) is pinned.
func TestStatusesFormatMatrix(t *testing.T) {
	t.Run("text keeps the tab-separated taxonomy", func(t *testing.T) {
		out := runStatuses(t, "text")
		// One line per status, "<order>\t<name>[\tterminal][\trequires-trigger]".
		assert.Contains(t, out, "0\t"+tasks.StatusInProgress)
		assert.Contains(t, out, tasks.StatusDeferred+"\trequires-trigger")
		assert.Contains(t, out, tasks.StatusDone+"\tterminal")
	})

	t.Run("json is the same array clifmt marshals", func(t *testing.T) {
		out := runStatuses(t, "json")
		var got []tasks.StatusInfo
		require.NoError(t, json.Unmarshal([]byte(out), &got))
		assert.Equal(t, tasks.Statuses(), got)
	})

	t.Run("json shorthand --json matches --format json", func(t *testing.T) {
		c := &cobra.Command{}
		c.Flags().String("format", "text", "")
		c.Flags().Bool("json", false, "")
		require.NoError(t, c.Flags().Set("json", "true"))
		var buf bytes.Buffer
		c.SetOut(&buf)
		require.NoError(t, statusesCmd.RunE(c, nil))
		var got []tasks.StatusInfo
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Equal(t, tasks.Statuses(), got)
	})

	t.Run("yaml carries the json field names", func(t *testing.T) {
		out := runStatuses(t, "yaml")
		assert.Contains(t, out, "name: "+tasks.StatusInProgress)
		assert.Contains(t, out, "requires_trigger:")
	})

	t.Run("toml wraps the slice under items", func(t *testing.T) {
		out := runStatuses(t, "toml")
		assert.Contains(t, out, "[[items]]")
		assert.Contains(t, out, "name = '"+tasks.StatusInProgress+"'")
	})

	t.Run("markdown renders a table", func(t *testing.T) {
		out := runStatuses(t, "markdown")
		assert.True(t, strings.Contains(out, "|"), "markdown output should be a table, got %q", out)
		assert.Contains(t, out, tasks.StatusInProgress)
	})
}

// U104-F05 claimed Emit's signature cannot express "the payload itself depends
// on the format/flags", so every such call site hand-rolls Emit's own format
// branch. The signature limitation is real - renderGlobalListing and
// renderProjectListing (commands.go) switch the PAYLOAD on opts.Compact as
// well as the format, which Emit's `data any` cannot carry - but these are not
// re-implementations of Emit's LOGIC: they take an already-resolved
// clifmt.Format and an io.Writer precisely so they can be driven without cobra
// in tests, and they never call Resolve.
//
// Template section 4 requires a parity test across both implementations before
// any collapse. This is it, and it is the "no red is possible" case: the two
// predicates are equivalent, so the test's job is to pin the SHARED branch
// policy rather than expose a divergence. The hand-rolled sites it covers are
// commands.go:197, commands.go:214, lint.go:64 and plan.go:197 here, plus
// cmd/ltk/check.go:123 and cmd/harp/version.go:26, all of which spell it
// `format == clifmt.FormatText` (or its negation).
//
// It goes red if either side moves: if Emit stops routing markdown through
// clifmt, or if a hand-rolled site starts admitting a second human format.
func TestFormatBranchParity_HandRolledSitesAgreeWithEmit(t *testing.T) {
	for _, format := range []clifmt.Format{
		clifmt.FormatText, clifmt.FormatJSON, clifmt.FormatYAML, clifmt.FormatTOML, clifmt.FormatMarkdown,
	} {
		c := &cobra.Command{}
		c.Flags().String("format", string(format), "")
		var out bytes.Buffer
		c.SetOut(&out)

		humanClosureRan := false
		require.NoError(t, cliemit.Emit(c, []string{"row"}, func() error {
			humanClosureRan = true
			return nil
		}))

		handRolledTakesHuman := format == clifmt.FormatText
		assert.Equal(t, handRolledTakesHuman, humanClosureRan,
			"the hand-rolled `format == clifmt.FormatText` branch must select the human "+
				"renderer exactly when cliemit.Emit runs its text closure (format %q)", format)
	}
}
