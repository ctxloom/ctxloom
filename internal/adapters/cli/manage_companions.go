package cli

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
)

// Companion-binary status for `manage check`. Companions are separate binaries
// that ctxloom never installs; a missing one contributes no loadout, so the
// status report names what is disabled and how to install it.

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

// hintForCompanion returns the install-hint text for bin, falling back to a
// generic description for companions without a curated entry.
func hintForCompanion(bin string) companionHint {
	if h, ok := companionHints[bin]; ok {
		return h
	}
	return companionHint{
		feature: "its built-in bundle wiring",
		install: "brew install ctxloom/tap/" + bin,
	}
}

// printCompanionStatus reports each companion binary's presence AND whether
// ctxloom is allowed to execute it; a missing one contributes no loadout.
//
// THIS REPORT RUNS NOTHING AND ASKS NOTHING, on every path, and that is a
// property of the command rather than of the fixture it happens to run in.
// Someone typing `ctxloom manage check` is asking what the state of things is,
// and the answer to that question must never be a trust-on-first-use question
// that changes the state of things. So this reads the admission decision from
// the allow store and never touches the resolved bundle set, whose companion
// reader IS the exec. An ALLOWED companion is not executed here either: a
// report has no use for what running it would produce.
//
// Presence alone is not the whole answer: a binary that is on PATH but not
// allowed is skipped, so printing its path
// and nothing else would tell the user everything is fine while the companion
// contributes nothing.
func printCompanionStatus(w io.Writer) {
	fmt.Fprintln(w, "Companions:")
	if App().NoCompanions {
		fmt.Fprintln(w, "  (companion discovery disabled for this run — --no-companions/CTXLOOM_NO_COMPANIONS)")
		return
	}
	for _, adm := range companions.AdmitCompanions(companions.DiscoverCompanions(), companions.LoadAllowed()) {
		hint := hintForCompanion(adm.Bin)
		switch {
		case adm.Path == "":
			fmt.Fprintf(w, "  %s: NOT FOUND — %s disabled (install: %s)\n", adm.Bin, hint.feature, hint.install)
		case !adm.Allow:
			fmt.Fprintf(w, "  %s: %s — NOT RUN (%s); %s disabled (to allow it: ctxloom companion allow %s)\n",
				adm.Bin, adm.Path, adm.Reason, hint.feature, adm.Path)
		default:
			fmt.Fprintf(w, "  %s: %s\n", adm.Bin, adm.Path)
		}
	}
}
