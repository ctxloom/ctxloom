package cli

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// Companion-binary status for `manage check`. Companions are separate binaries
// that ctxloom never installs; a registered one that is missing contributes no
// loadout, so the status report names what is disabled and how to install it.

// companionHint describes what a missing companion binary disables and how to
// install it.
type companionHint struct {
	feature string
	install string
}

// companionHints carries the per-companion status text. An entry missing from
// this map gets the generic fallback, so a companion nobody curated text for is
// still reported rather than dropped.
var companionHints = map[string]companionHint{
	"taskloom": {"task tools (task_list/task_add/...)", "brew install ctxloom/tap/taskloom"},
	"ltk":      {"command-redirect pre-tool hook", "brew install ctxloom/tap/ltk"},
}

// hintForCompanion returns the install-hint text for a companion name,
// falling back to a generic description for companions without a curated
// entry.
func hintForCompanion(name string) companionHint {
	if h, ok := companionHints[name]; ok {
		return h
	}
	return companionHint{
		feature: "its loadout",
		install: "put its binary on PATH",
	}
}

// printCompanionStatus reports each REGISTERED companion (names) and whether
// its binary resolves on PATH; a missing one contributes no loadout.
//
// THIS REPORT RUNS NOTHING, on every path, and that is a property of the
// command rather than of the fixture it happens to run in: it resolves names
// on PATH and never touches the resolved bundle set, whose companion reader IS
// the exec. A report has no use for what running a companion would produce.
func printCompanionStatus(w io.Writer, names []string) {
	fmt.Fprintln(w, "Companions:")
	if App().NoCompanions {
		fmt.Fprintln(w, "  (companions disabled for this run — --no-companions/CTXLOOM_NO_COMPANIONS)")
		return
	}
	if len(names) == 0 {
		fmt.Fprintln(w, "  (none registered — ctxloom companion add <name>)")
		return
	}
	for _, l := range operations.ListCompanions(names) {
		hint := hintForCompanion(l.Name)
		if !l.Resolves {
			fmt.Fprintf(w, "  %s: NOT FOUND (%s) — %s disabled (install: %s; or unregister: ctxloom companion remove %s --yes)\n",
				l.Name, l.Bin, hint.feature, hint.install, l.Name)
			continue
		}
		fmt.Fprintf(w, "  %s: %s\n", l.Name, l.Path)
	}
}
