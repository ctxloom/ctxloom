package tasks

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// swapTaxonomy replaces the package's status taxonomy for the duration of a
// test and restores it afterwards. It is the instrument the live-expansion
// proof needs: a class that really reads the taxonomy at runtime must follow
// a status ADDED here, with no edit to statusclass.go.
func swapTaxonomy(t *testing.T, entries ...StatusInfo) {
	t.Helper()
	orig := statusTaxonomy
	t.Cleanup(func() { statusTaxonomy = orig })
	statusTaxonomy = entries
}

// TestExpandStatusClasses_FollowsTheLiveTaxonomy is the load-bearing test for
// uneven-faction: it ADDS statuses to the taxonomy and requires the classes to
// pick them up. Nothing in statusclass.go names a status, so a hardcoded
// expansion cannot pass this — which is the point. Asserting the expansion
// equals a fixed set of today's five names would prove only that someone
// typed the same list twice.
func TestExpandStatusClasses_FollowsTheLiveTaxonomy(t *testing.T) {
	before, err := ExpandStatusClasses([]string{StatusClassOpen})
	require.NoError(t, err)
	require.NotContains(t, before, "Blocked")

	swapTaxonomy(t,
		StatusInfo{Name: StatusInProgress},
		StatusInfo{Name: StatusToDo},
		StatusInfo{Name: "Blocked"},
		StatusInfo{Name: StatusDeferred, RequiresTrigger: true},
		StatusInfo{Name: StatusDone, Terminal: true},
		StatusInfo{Name: "Shipped", Terminal: true},
	)

	open, err := ExpandStatusClasses([]string{StatusClassOpen})
	require.NoError(t, err)
	require.Contains(t, open, "Blocked", "@open must pick up a non-terminal status added to the taxonomy, with no change to the class-expansion code")
	require.NotContains(t, open, "Shipped")
	require.NotContains(t, open, StatusDone)

	terminal, err := ExpandStatusClasses([]string{StatusClassTerminal})
	require.NoError(t, err)
	require.Contains(t, terminal, "Shipped", "@terminal must pick up a terminal status added to the taxonomy")
	require.Contains(t, terminal, StatusDone)
	require.NotContains(t, terminal, "Blocked")

	// Together the two classes partition the taxonomy — no status is in
	// neither, none in both.
	require.ElementsMatch(t, taxonomyNames(), append(append([]string{}, open...), terminal...))
}

func taxonomyNames() []string {
	out := make([]string, 0, len(statusTaxonomy))
	for _, s := range Statuses() {
		out = append(out, s.Name)
	}
	return out
}

// TestExpandStatusClasses_PreservesLiterals pins that this is SUGAR: plain
// status values pass through untouched, an empty filter stays empty (an
// expansion that manufactured values would flip a caller's default
// active-only view into an explicit filter), and classes mix with literals.
func TestExpandStatusClasses_PreservesLiterals(t *testing.T) {
	t.Run("empty stays empty", func(t *testing.T) {
		got, err := ExpandStatusClasses(nil)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("plain values pass through in order", func(t *testing.T) {
		got, err := ExpandStatusClasses([]string{StatusDone, StatusToDo})
		require.NoError(t, err)
		require.Equal(t, []string{StatusDone, StatusToDo}, got)
	})

	t.Run("a class mixes with a literal", func(t *testing.T) {
		got, err := ExpandStatusClasses([]string{StatusClassTerminal, StatusInProgress})
		require.NoError(t, err)
		require.Contains(t, got, StatusDone)
		require.Contains(t, got, StatusArchived)
		require.Contains(t, got, StatusInProgress)
	})

	t.Run("an overlap between a class and a literal is collapsed", func(t *testing.T) {
		got, err := ExpandStatusClasses([]string{StatusClassTerminal, StatusDone})
		require.NoError(t, err)
		require.Equal(t, 1, countOf(got, StatusDone), "%v repeats a status", got)
	})
}

func countOf(list []string, want string) int {
	n := 0
	for _, v := range list {
		if v == want {
			n++
		}
	}
	return n
}

// TestExpandStatusClasses_FailsLoud covers the two ways a class could quietly
// narrow a listing to nothing: an unrecognized @-value, and a class that
// currently selects no status at all (which would expand to an empty filter
// and read as "no filter" — the opposite of what was asked).
func TestExpandStatusClasses_FailsLoud(t *testing.T) {
	t.Run("unknown class", func(t *testing.T) {
		_, err := ExpandStatusClasses([]string{"@done"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "@done")
		require.Contains(t, err.Error(), StatusClassOpen, "the error must name the classes that do exist")
	})

	t.Run("class matching nothing", func(t *testing.T) {
		swapTaxonomy(t, StatusInfo{Name: StatusDone, Terminal: true})
		_, err := ExpandStatusClasses([]string{StatusClassOpen})
		require.Error(t, err)
		require.Contains(t, err.Error(), StatusClassOpen)
	})
}

// TestValidateStatusName_ReservesTheSigil pins the reservation itself.
func TestValidateStatusName_ReservesTheSigil(t *testing.T) {
	require.NoError(t, ValidateStatusName(StatusToDo))
	require.NoError(t, ValidateStatusName("Blocked"))
	require.NoError(t, ValidateStatusName("needs@review"), "the sigil is reserved as a PREFIX, not banned outright")

	for _, bad := range []string{"@open", "@", "@Whatever"} {
		err := ValidateStatusName(bad)
		require.Error(t, err, "status %q must be refused", bad)
		require.Contains(t, err.Error(), StatusClassSigil)
	}
}

// TestStoreRefusesSigilStatus pins the reservation at the WRITE seam, where
// it actually matters: an @-prefixed status must never reach the log, on
// either the add or the status-change path, or it would sit in the store
// forever, unfilterable because every filter reads it as a class.
func TestStoreRefusesSigilStatus(t *testing.T) {
	store, err := OpenLog(filepath.Join(t.TempDir(), "tasks.jsonl"), "")
	require.NoError(t, err)

	_, err = store.AddWithTags("some work", "@open", "", nil...)
	require.Error(t, err, "add must refuse an @-prefixed status")
	require.Contains(t, err.Error(), StatusClassSigil)

	task, err := store.AddWithTags("some work", "", "", nil...)
	require.NoError(t, err)

	_, err = store.SetStatusWithTrigger(task.HarpID, "@terminal", "")
	require.Error(t, err, "status change must refuse an @-prefixed status")
	require.Contains(t, err.Error(), StatusClassSigil)

	// And the refusal left nothing behind.
	all, err := store.Snapshot()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, StatusToDo, all[0].Status)
	for _, tk := range all {
		require.False(t, strings.HasPrefix(tk.Status, StatusClassSigil))
	}
}

// TestIsTerminalStatusReadsTheTaxonomy pins that the terminal predicate and
// the taxonomy cannot disagree — the second hand-maintained copy of "which
// statuses are terminal" is what its own doc comment warns about.
func TestIsTerminalStatusReadsTheTaxonomy(t *testing.T) {
	require.False(t, IsTerminalStatus("Shipped"))
	swapTaxonomy(t, StatusInfo{Name: StatusToDo}, StatusInfo{Name: "Shipped", Terminal: true})
	require.True(t, IsTerminalStatus("Shipped"))
	require.False(t, IsTerminalStatus(StatusToDo))
	require.False(t, IsTerminalStatus("Whatever"), "a name the taxonomy never heard of is not terminal")
}
