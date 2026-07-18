package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
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
