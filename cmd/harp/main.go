// Command harp generates Human Appropriate Random Phraselets — pronounceable,
// memorable identifiers of the form "swift-amber-falcon".
//
// It is a thin, standalone CLI over internal/shared/harp, the generator
// ctxloom itself uses in-process for session and task IDs. ctxloom never
// shells out to this binary — it imports the library directly — so this
// command exists purely for ad-hoc and external use (scripts, other tools,
// interactive terminals) outside a ctxloom process. It replaces the former
// `ctxloom harp` subcommand, removed in favor of this independently
// distributable binary.
package main

import (
	"fmt"
	"os"
)

// Version is set at build time via ldflags (package main), e.g.
//
//	-X main.Version=v1.2.3
//
// It defaults to "dev"; cmd/harp/justfile stamps it from versionator,
// matching the ltk/taskloom family convention.
var Version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, progName+":", err)
		os.Exit(1)
	}
}
