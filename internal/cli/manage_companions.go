package cli

import (
	"fmt"
	"os/exec"

	"github.com/ctxloom/ctxloom/internal/config"
)

// Companion-binary status for `manage status`. Companions are separate binaries
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

// printCompanionStatus reports each companion binary's presence; builtin
// bundle entries for missing ones are skipped at resolve time.
func printCompanionStatus() {
	fmt.Println("Companions:")
	for _, bin := range config.BuiltinCompanionBins() {
		hint := hintForCompanion(bin)
		path, err := exec.LookPath(bin)
		if err != nil {
			fmt.Printf("  %s: NOT FOUND — %s disabled (install: %s)\n", bin, hint.feature, hint.install)
			continue
		}
		fmt.Printf("  %s: %s\n", bin, path)
	}
}
