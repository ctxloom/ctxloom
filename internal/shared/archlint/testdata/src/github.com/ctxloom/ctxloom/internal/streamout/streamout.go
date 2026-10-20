// Package streamout sits outside the rule's scopes.
package streamout

import (
	"fmt"
	"os"
)

// Say writes to stderr, unjudged.
func Say() { fmt.Fprintln(os.Stderr, "out of scope") }
