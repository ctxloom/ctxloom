// Package streamcli is under the CLI, which owns the terminal.
package streamcli

import (
	"fmt"
	"os"
)

// Banner is the CLI's own output.
func Banner() { fmt.Fprintln(os.Stderr, "banner") }
