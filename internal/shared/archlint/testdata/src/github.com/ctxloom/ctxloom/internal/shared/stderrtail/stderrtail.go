// Package stderrtail stands at a path archrules.StdStreamsAllowed names, so
// its reasoned exception is honoured.
package stderrtail

import (
	"io"
	"os"
)

// TeeStderr is the allowlisted symbol.
func TeeStderr() io.Writer { return os.Stderr }
