package cli

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// resetFlags returns every flag of every command under root — Flags() and
// PersistentFlags() alike — to its declared default, and clears Changed.
//
// root is a process-global tree that every test in this package dispatches
// through, and pflag never un-sets anything: a value a test set survives into
// the next test, and so does the Changed bit, which is what cobra's
// MarkFlagsMutuallyExclusive and every "was it passed?" check read. Both are
// restored for EVERY flag, not just the Changed ones, because a test that
// Sets a value and clears Changed by hand leaves a non-default value with
// Changed false. Restoring through the flag's Value also restores the
// package-level var it is bound to.
//
// A slice flag is restored with Replace, never Set: once a slice value has
// been set, its Set APPENDS. Replace does not clear the value's own internal
// "set" bit, so the next parse still appends to what Replace left — which is
// only equivalent to a fresh process because every slice default in the tree
// is empty (TestResetFlags_EverySliceDefaultIsEmpty holds that).
//
// It also turns off the structured-diagnostics channel, which the root's
// PersistentPreRun switches on for a json/yaml/toml --format and never
// switches off: that is --format's state too.
func resetFlags(t testing.TB, root *cobra.Command) {
	t.Helper()
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			require.NoError(t, sv.Replace(sliceDefValue(t, f)), "reset --%s", f.Name)
		} else {
			require.NoError(t, f.Value.Set(f.DefValue), "reset --%s", f.Name)
		}
		f.Changed = false
	}
	walkCommands(root, func(c *cobra.Command) {
		c.Flags().VisitAll(reset)
		c.PersistentFlags().VisitAll(reset)
	})
	clidiag.SetStructured(false)
}

// sliceDefValue parses a slice flag's DefValue — pflag renders it as the
// elements in CSV form wrapped in brackets — back into its elements.
func sliceDefValue(t testing.TB, f *pflag.Flag) []string {
	t.Helper()
	body := strings.TrimSuffix(strings.TrimPrefix(f.DefValue, "["), "]")
	if body == "" {
		return nil
	}
	elems, err := csv.NewReader(strings.NewReader(body)).Read()
	require.NoError(t, err, "parse --%s default %q", f.Name, f.DefValue)
	return elems
}

// TestResetFlags_EverySliceDefaultIsEmpty holds the invariant resetFlags'
// slice restore depends on; see resetFlags.
func TestResetFlags_EverySliceDefaultIsEmpty(t *testing.T) {
	walkCommands(rootCmd, func(c *cobra.Command) {
		visit := func(f *pflag.Flag) {
			if _, ok := f.Value.(pflag.SliceValue); ok {
				assert.Empty(t, sliceDefValue(t, f), "%s --%s", c.CommandPath(), f.Name)
			}
		}
		c.Flags().VisitAll(visit)
		c.PersistentFlags().VisitAll(visit)
	})
}

// Every flag in the real tree must survive a reset: a Value whose Set rejects
// its own DefValue would make resetFlags fail every test that calls it.
func TestResetFlags_EveryRealFlagRoundTripsItsDefault(t *testing.T) {
	resetFlags(t, rootCmd)
}

// A test that Sets a flag and then clears Changed by hand — the shape of a
// "second invocation that does not pass the flag" — leaves a non-default
// value behind. The reset must restore it anyway: Changed says whether argv
// named the flag, not whether the value is the default.
func TestResetFlags_RestoresDefaultsEvenWhenChangedWasClearedByHand(t *testing.T) {
	const (
		scalarDefault = "scalar-default"
		sliceDefault  = "slice-default"
	)
	var scalar string
	var slice []string
	var toggle bool
	root := &cobra.Command{Use: "root"}
	child := &cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}}
	root.AddCommand(child)
	root.PersistentFlags().StringVar(&scalar, "scalar", scalarDefault, "")
	child.Flags().StringSliceVar(&slice, "slice", []string{sliceDefault}, "")
	child.Flags().BoolVar(&toggle, "toggle", false, "")

	require.NoError(t, root.PersistentFlags().Set("scalar", "other"))
	require.NoError(t, child.Flags().Set("slice", "other"))
	require.NoError(t, child.Flags().Set("toggle", "true"))
	for _, f := range []string{"slice", "toggle"} {
		child.Flags().Lookup(f).Changed = false
	}
	root.PersistentFlags().Lookup("scalar").Changed = false

	resetFlags(t, root)

	assert.Equal(t, scalarDefault, scalar)
	assert.Equal(t, []string{sliceDefault}, slice)
	assert.False(t, toggle)
	for _, f := range []*cobra.Command{root, child} {
		f.Flags().VisitAll(func(fl *pflag.Flag) { assert.False(t, fl.Changed, "--%s", fl.Name) })
	}
}
