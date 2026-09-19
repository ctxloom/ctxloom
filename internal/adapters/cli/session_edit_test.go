package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resetSessionEditFlags restores the package-level cobra flag var backing
// `session edit`, and clidiag's process-wide structured flag. pflag only
// calls Set() on flags present in a given argv and never calls Changed(false)
// afterwards, so a --name a prior test passed survives into a later
// invocation that never mentions it — the same leak session_worktrees_test.go
// documents at length.
func resetSessionEditFlags() {
	sessionEditName = ""
	_ = sessionEditCmd.Flags().Set("name", "")
	sessionEditCmd.Flags().Lookup("name").Changed = false
	clidiag.SetStructured(false)
}

// TestSessionEdit_NameAssignsTheHarp is the rename path in its new spelling:
// renaming a session is a field assignment, so it rides `edit --name` rather
// than a verb of its own.
func TestSessionEdit_NameAssignsTheHarp(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	t.Cleanup(resetSessionEditFlags)

	out, err := execRootCmd(t, "session", "edit", harp, "--name", "bright-keen-hawk", "--format", "json")
	require.NoError(t, err)

	var got struct {
		Harp         string   `json:"harp"`
		PreviousHarp string   `json:"previous_harp"`
		Fields       []string `json:"fields"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	assert.Equal(t, "bright-keen-hawk", got.Harp)
	assert.Equal(t, harp, got.PreviousHarp)
	assert.Equal(t, []string{"name"}, got.Fields)

	// The index is the payload assertion: a report that says "renamed" while
	// the index still answers to the old name is this project's silent no-op.
	renamed, err := mgr.Find("bright-keen-hawk")
	require.NoError(t, err)
	require.NotNil(t, renamed, "the new name must resolve in the index")
	old, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.Nil(t, old, "the old name must no longer resolve")
}

// TestSessionEdit_TextReportNamesBothSpellings pins the human render: a
// rename that does not say what it renamed FROM leaves an operator unable to
// undo it.
func TestSessionEdit_TextReportNamesBothSpellings(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	t.Cleanup(resetSessionEditFlags)

	out, err := execRootCmd(t, "session", "edit", harp, "--name", "bright-keen-hawk")
	require.NoError(t, err)
	assert.Contains(t, out, harp)
	assert.Contains(t, out, "bright-keen-hawk")
}

// TestSessionEdit_BareRefusesLoudly is the escalated decision, pinned. A
// session is a RECORD of something that happened: its index entry is
// machine-written and its essence is DERIVED, regenerated wholesale by
// `session distill`. There is no document for $EDITOR to open, so the bare
// form refuses and names the assignments it does take, rather than opening
// something whose edits the next distill would silently discard.
func TestSessionEdit_BareRefusesLoudly(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	t.Cleanup(resetSessionEditFlags)

	_, err := execRootCmd(t, "session", "edit", harp)
	require.Error(t, err, "a bare `session edit` must refuse, never exit 0 having changed nothing")
	assert.Contains(t, err.Error(), "--name", "the refusal must name the assignment it does accept")
	assert.Contains(t, err.Error(), "no editable document")
}

// TestSessionEdit_UnknownHarpFails matches `session distill`'s convention:
// naming a harp nothing knows is an error, not a silent success.
func TestSessionEdit_UnknownHarpFails(t *testing.T) {
	testsupport.ProjectDir(t)
	t.Cleanup(resetSessionEditFlags)

	_, err := execRootCmd(t, "session", "edit", "no-such-harp", "--name", "bright-keen-hawk")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-harp")
}

// TestSessionEdit_RenameLeafIsGone pins the deletion: `session rename` is not
// a hidden alias, it is not there at all.
func TestSessionEdit_RenameLeafIsGone(t *testing.T) {
	for _, c := range sessionCmd.Commands() {
		assert.NotEqual(t, "rename", c.Name(), "`session rename` is replaced by `session edit --name`")
	}
}
