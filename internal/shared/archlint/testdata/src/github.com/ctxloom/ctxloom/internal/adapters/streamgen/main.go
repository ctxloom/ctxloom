// Command streamgen is a binary: its main package owns its own streams.
package main

import (
	"fmt"
	"os"
)

func main() { fmt.Fprintln(os.Stderr, "generated") }
