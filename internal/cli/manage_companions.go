package cli

import (
	"fmt"

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

// companionHints carries the per-companion status text. The companion list
// itself is derived from the embedded built-in bundles (see
// config.BuiltinCompanionBins) so a future builtin's companion shows up here
// automatically; an entry missing from this map gets the generic fallback.
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
// Presence alone stopped being the whole answer when exec consent landed: a
// binary that is on PATH but never confirmed is skipped, so printing its path
// and nothing else would tell the user everything is fine while the companion
// contributes nothing. It asks AdmitCompanions with prompt=false — reading a
// status report must never conjure a security question.
func printCompanionStatus() {
	fmt.Println("Companions:")
	for _, adm := range config.AdmitCompanions(config.BuiltinCompanionBins(), false) {
		hint := hintForCompanion(adm.Bin)
		switch {
		case adm.Path == "":
			fmt.Printf("  %s: NOT FOUND — %s disabled (install: %s)\n", adm.Bin, hint.feature, hint.install)
		case !adm.Allow:
			fmt.Printf("  %s: %s — NOT RUN (%s); %s disabled (allow: ctxloom companion trust %s)\n",
				adm.Bin, adm.Path, adm.Reason, hint.feature, adm.Path)
		default:
			fmt.Printf("  %s: %s\n", adm.Bin, adm.Path)
		}
	}
}
