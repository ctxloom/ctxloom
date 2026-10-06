package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/envswitch"
	"github.com/ctxloom/ctxloom/internal/shared/logboot"
	"github.com/ctxloom/ctxloom/internal/shared/mountns"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

func main() {
	// Become the mount shim, if that is what this process was spawned to be.
	// A re-exec of ourselves is the only way to run code between clone(2) and
	// execve(2) (see internal/shared/mountns), so a namespace-mounted run
	// arrives here as an ordinary ctxloom process carrying a marker. Returns
	// immediately for every other process and never returns for a shim, which
	// is why it sits before dispatch rather than inside a command: a shim that
	// reached cobra would parse flags meant for the engine.
	mountns.RunChildIfRequested()

	// Companion discovery off from the environment, read BEFORE dispatch for the
	// same reason: the pre-cobra window can already assemble context, and probing
	// EXECUTES the companion binaries on PATH. CTXLOOM_NO_COMPANIONS=1 is the
	// mechanism for a subprocess/CI that must not depend on what the host has
	// installed; the persistent --no-companions flag wins over it once parsed
	// (see cli root's PersistentPreRun).

	// Install the process logger (verbose mode if CTXLOOM_VERBOSE=1) before
	// composing, so anything composition logs is kept; then dispatch, flush,
	// exit — in that order, with the exit as the LAST thing this process does
	// (see logboot.Install for why the flush cannot be a defer).
	flush := logboot.Install("ctxloom", envSwitchOn("CTXLOOM_VERBOSE", os.Stderr))
	// ctxloom's root is the filesystem this process runs on, built once here
	// and threaded down; its private home roots are established before any
	// command can write beneath them (paths.EnsureHomeRoots).
	root := safefs.New()
	if err := paths.EnsureHomeRoots(root.Private); err != nil {
		clidiag.Warn("ctxloom", "%v", err)
	}
	comp := compose(strictness.Sink("ctxloom"), root)
	code := cli.Run(comp)
	flush()
	os.Exit(code)
}

// envSwitchOn reads one of the CTXLOOM_* boolean process switches and reports
// a value no boolean spelling covers, rather than treating it as off in
// silence. These switches are read before any flag is parsed, so this warning
// is the only feedback an operator who mistyped one will ever get: the mode
// simply would not engage, with nothing to distinguish that from the feature
// being broken.
func envSwitchOn(name string, warn io.Writer) bool {
	on, unrecognized := envswitch.On(name)
	if unrecognized != "" && warn != nil {
		fmt.Fprintf(warn, "ctxloom: warning: %s=%q is not an on/off value; treating it as off "+
			"(on: 1/true/yes/on, off: 0/false/no/off)\n", name, unrecognized)
	}
	return on
}
