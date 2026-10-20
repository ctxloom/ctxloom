// Package streamplant writes to the process's own standard streams from
// library code, every spelling the rule knows.
package streamplant

import (
	"fmt"
	"io"
	"log"
	stdos "os"
	"os/exec"
)

var sink = stdos.Stderr // want `internal/adapters/streamplant/streamplant.go writes to the process's own standard stream \(os.Stderr\)`

// Announce prints a notice straight to the terminal.
func Announce(name string) {
	fmt.Fprintf(stdos.Stderr, "container %s\n", name) // want `\(os.Stderr\)`
	_, _ = stdos.Stdout.WriteString(name)             // want `\(os.Stdout\)`
	fmt.Println(name)                                 // want `\(fmt.Println\)`
	log.Printf("%s", name)                            // want `\(log.Printf\)`
	print(name)                                       // want `\(print\)`
	println(stdos.Stderr)                             // want `\(println\)` `\(os.Stderr\)`
	_ = sink
}

// Capture names nothing that prints: a field named Stderr, another os
// variable, a logger's own method named like a default-logger printer, a
// universe method, a builtin that is not print, and formatting to a string.
func Capture(w fmt.Stringer, err error) string {
	cmd := exec.Command("true")
	cmd.Stderr = nil
	_ = stdos.Args
	log.New(io.Discard, "", 0).Printf("quiet")
	_ = len(err.Error())
	return fmt.Sprintf("%v", w)
}
