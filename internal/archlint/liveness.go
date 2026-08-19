package archlint

import (
	"os"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// allowlistLivenessEnv turns off the allowlist-liveness half of every rule.
//
// A liveness check asserts that an exemption still describes a real violation,
// so it is only meaningful in a pass that can SEE the exempted code. Under a
// build-tag set that hides the file, "the scan no longer reports it" is not a
// stale entry — it is a pass that was never looking. Suppressing liveness is
// how a supplementary pass, run under a narrower tag set to reach files the
// main pass hides, avoids reporting every exemption it cannot see as rotten.
const allowlistLivenessEnv = "ARCHLINT_CHECK_ALLOWLISTS"

// allowlistLivenessEnabled reports whether liveness checks should run. On by
// default: the failure mode of skipping them silently is an exemption that
// outlives what it exempted.
func allowlistLivenessEnabled() bool {
	return os.Getenv(allowlistLivenessEnv) != "0"
}

// reportStaleAllowlist fails an allowlist entry that names a site in one of
// the files this pass actually analyzed, which the scan no longer reports.
//
// A stale exception is worse than none: left in place it silently exempts
// whatever lands at that key next, and the ratchet could never tighten.
//
// Entries naming a file this pass did not analyze are skipped rather than
// reported. The file may be real and merely hidden by the current build tags,
// and a rule that cannot see a file must not pass judgement on it.
func reportStaleAllowlist(pass *analysis.Pass, allowed map[string]string, analyzed, seen map[string]bool, mapName, mapFile string) {
	if !allowlistLivenessEnabled() {
		return
	}
	for key, why := range allowed {
		file, _, ok := strings.Cut(key, "#")
		if !ok || !analyzed[file] || seen[key] {
			continue
		}
		pass.Reportf(pass.Files[0].Package,
			"%s names %q (%s) but the scan no longer reports it — delete the entry, or it will silently "+
				"exempt whatever lands at that key next (%s)", mapName, key, why, mapFile)
	}
}

// analyzedFiles is the set of module-relative production files in this pass,
// the only files a liveness check is entitled to judge.
func analyzedFiles(pass *analysis.Pass) map[string]bool {
	out := map[string]bool{}
	for _, f := range ProdFiles(pass) {
		out[FileRel(pass, f)] = true
	}
	return out
}
