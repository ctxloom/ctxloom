package cli

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/config"
)

// Companion-binary status for `manage check`. Companions are separate binaries
// (taskloom, ltk) that ctxloom never installs; a missing one silently disables
// the builtin bundle wiring that needs it, so the status report names what is
// disabled and how to install it.

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
// ctxloom is allowed to execute it; builtin bundle entries for missing ones are
// skipped at resolve time.
//
// THIS REPORT RUNS NOTHING AND ASKS NOTHING, on every path, and that is a
// property of the command rather than of the fixture it happens to run in.
// Someone typing `ctxloom manage check` is asking what the state of things is,
// and the answer to that question must never be a trust-on-first-use question
// that changes the state of things — a consent prompt raised in the middle of a
// report is one a reader is primed to dismiss, which is how a security decision
// becomes a rubber stamp. So this reads the admission decision with prompt
// FALSE (the arm that leaves admission.Ask nil, so there is nothing to ask
// with) and never touches the resolved bundle set, whose companion reader IS
// the exec. An APPROVED companion is not executed here either: a report has no
// use for what running it would produce.
//
// Presence alone stopped being the whole answer when exec consent landed: a
// binary that is on PATH but never confirmed is skipped, so printing its path
// and nothing else would tell the user everything is fine while the companion
// contributes nothing.
func printCompanionStatus(w io.Writer) {
	fmt.Fprintln(w, "Companions:")
	if config.CompanionsDisabled() {
		fmt.Fprintln(w, "  (companion discovery disabled for this run — --no-companions/CTXLOOM_NO_COMPANIONS)")
		return
	}
	for _, adm := range config.AdmitCompanions(config.DiscoverCompanions(), false) {
		hint := hintForCompanion(adm.Bin)
		switch {
		case adm.Path == "":
			fmt.Fprintf(w, "  %s: NOT FOUND — %s disabled (install: %s)\n", adm.Bin, hint.feature, hint.install)
		case !adm.Allow:
			fmt.Fprintf(w, "  %s: %s — NOT RUN (%s); %s disabled (allow: ctxloom companion trust %s)\n",
				adm.Bin, adm.Path, adm.Reason, hint.feature, adm.Path)
		default:
			fmt.Fprintf(w, "  %s: %s\n", adm.Bin, adm.Path)
		}
	}
}
