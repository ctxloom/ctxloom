package cli

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/trust"
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

// printCompanionStatus reports which companions are contributing content and
// which are not, from the resolved catalog cat.
//
// It reads the catalog rather than discovering companions itself, and that is
// the whole point: one pass over $PATH and the consent record decides what a
// session carries, so a report derived from a SECOND pass can disagree with the
// session it claims to describe. The reads are what is contributing; the
// candidates are the identities that produced nothing, each with the reason.
//
// Presence alone is not the answer. A binary that is on PATH but never
// confirmed for execution is skipped, so printing its path and nothing else
// would say everything is fine while the companion contributes nothing.
func printCompanionStatus(cat bundles.Catalog) {
	fmt.Println("Companions:")
	if config.CompanionsDisabled() {
		fmt.Println("  (companion discovery disabled for this run — --no-companions/CTXLOOM_NO_COMPANIONS)")
		return
	}
	printed := 0
	for _, read := range cat.Reads() {
		if read.Provenance != bundles.ProvenanceCompanion {
			continue
		}
		fmt.Printf("  %s: contributing its loadout\n", companionBinOf(read.Key()))
		printed++
	}
	for _, cand := range cat.Candidates() {
		bin := companionBinOf(cand.Ref)
		hint := hintForCompanion(bin)
		switch cand.Reason {
		case bundles.CandidateAbsent:
			fmt.Printf("  %s: NOT FOUND — %s disabled (install: %s)\n", bin, hint.feature, hint.install)
		case bundles.CandidateUnconsented:
			fmt.Printf("  %s: %s — NOT RUN (never allowed to execute); %s disabled (allow: ctxloom companion trust %s)\n",
				bin, cand.Path, hint.feature, cand.Path)
		default:
			fmt.Printf("  %s: %s — probe failed; %s disabled (see the warnings above)\n",
				bin, cand.Path, hint.feature)
		}
		printed++
	}
	if printed == 0 {
		fmt.Println("  (none discovered)")
	}
}

// companionBinOf recovers the binary name a companion identity was minted
// from. A key that will not parse is shown verbatim: it is still the most
// specific thing known about that entry, and hiding it would drop a row.
func companionBinOf(key trust.BundleKey) string {
	ref, err := trust.ParseBundleRef(string(key))
	if err != nil || ref.Bundle == "" {
		return string(key)
	}
	return ref.Bundle
}

// printCompanionSection resolves the session's bundle set and renders the
// companion report from it.
//
// The resolution happens HERE rather than at the command, and only on the path
// that renders: resolving reads every source and execs every companion this
// machine's human has approved, which a caller asking for the machine-readable
// form has not asked for. GetConfig is the same memoized config every other
// command in this package reads, so this shares one parse and one probe with
// the rest of the run rather than starting a second.
func printCompanionSection() {
	cfg, err := GetConfig()
	if err != nil {
		fmt.Println("Companions:")
		fmt.Printf("  (unavailable: %v)\n", err)
		return
	}
	printCompanionStatus(cfg.BundleLoader().Catalog())
}
