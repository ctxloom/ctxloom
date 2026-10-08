package main

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/formatparity"
)

// ltk's --format plumbing is the family's: the same help, completion,
// default and error tail as every other binary (formatparity.Check).
func TestFormatParity(t *testing.T) {
	formatparity.Check(t, formatparity.Binary{
		Prog: progName,
		Root: newRootCmd,
		Run: func(t *testing.T, args ...string) (int, string) {
			return runMainForExitStatus(t, strings.Join(args, " "))
		},
		// check takes no arguments; the extra one fails argument validation
		// before any file or config is read.
		FailingArgs: []string{"check", "zz-extra-arg"},
	})
}
