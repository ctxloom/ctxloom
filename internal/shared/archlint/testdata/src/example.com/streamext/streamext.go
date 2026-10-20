// Package streamext is outside the module.
package streamext

import (
	"fmt"
	"os"
)

// Say writes to stderr, unjudged.
func Say() { fmt.Fprintln(os.Stderr, "not ours") }
