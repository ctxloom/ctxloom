package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resetRootFormat puts rootCmd's persistent --format back to its default.
// rootCmd is package-global and pflag never un-sets a flag a prior test set,
// so a neighbour's `--format json` otherwise decides this test's answer — the
// same leak runRoot documents in group_node_test.go.
func resetRootFormat(t *testing.T) {
	t.Helper()
	if f := rootCmd.PersistentFlags().Lookup("format"); f != nil {
		require.NoError(t, f.Value.Set(f.DefValue))
		f.Changed = false
	}
	clidiag.SetStructured(false)
}

// A bare namespace that answers with a listing must honor --format, exactly as
// the named leaf does. `ctxloom session --format json` used to print the text
// table and exit 0: a caller that asked for JSON to parse got a table, with
// nothing in the status to say so — the same silent-no-op shape
// groupNodeFormatRefusal exists to close for a namespace that has no listing
// at all, arriving here through the other door.
//
// The cause is structural rather than a missing check. runGroupNodeDefault
// invokes the child's RunE directly, and cobra only merges a command's
// inherited persistent flags during ParseFlags — which the child, never having
// been dispatched to, never runs. So the child's own Flags() had no "format"
// at all and cliemit.Resolve fell through to text.
func TestGroupNodeDefault_BareNounHonorsFormat(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	resetRootFormat(t)
	t.Cleanup(func() { resetRootFormat(t) })

	out, err := execRootCmd(t, "session", "--format", "json")
	require.NoError(t, err)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows),
		"a bare noun asked for json must emit json, not a text table: %s", out)
	require.Len(t, rows, 1)
	assert.Equal(t, harp, rows[0]["harp"])
}

// The text default is unchanged: no --format still renders the human table.
func TestGroupNodeDefault_BareNounStillDefaultsToText(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	resetRootFormat(t)
	t.Cleanup(func() { resetRootFormat(t) })

	out, err := execRootCmd(t, "session")
	require.NoError(t, err)
	assert.Contains(t, out, "HARP")
	assert.Contains(t, out, harp)
}
