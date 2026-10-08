package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/testsupport/formatparity"
)

// taskloom's --format plumbing is the family's: the same help, completion,
// default and error tail as every other binary (formatparity.Check).
func TestFormatParity(t *testing.T) {
	formatparity.Check(t, formatparity.Binary{
		Prog: progName,
		Root: func() *cobra.Command { return rootCmd },
		Run: func(t *testing.T, args ...string) (int, string) {
			return runMainForExitStatus(t, strings.Join(args, " "))
		},
		// show takes exactly one argument; omitting it fails before any
		// store is opened.
		FailingArgs: []string{"show"},
	})
}
