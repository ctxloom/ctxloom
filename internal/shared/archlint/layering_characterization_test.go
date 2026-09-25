package archlint

import (
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// layeringTable is a small rules table standing in for archrules.LayeringRules,
// so each branch of runLayering is reached by a fixture written for it rather
// than by whatever the production table happens to hold.
//
// "t-rule" forbids layerlow to layerfrom and layerquiet, excepting
// layerlow/okay. Its Allowed map holds one live edge, one naming an unforbidden
// package, one naming a forbidden package no longer imported, one keyed to a
// different from-package, one malformed key, and one stale entry keyed to
// layerquiet that only the liveness-off test analyzes. "t-other" does not cover
// layerfrom, so it reports no import yet still owns an entry naming layerfrom.
var layeringTable = []archrules.LayeringRule{
	{
		Name:   "t-rule",
		From:   []string{"internal/layerfrom", "internal/layerquiet"},
		Forbid: []string{"internal/layerlow"},
		Except: []string{"internal/layerlow/okay"},
		Allowed: map[string]string{
			"internal/layerfrom -> internal/layerlow/allowed": "live-why",
			"internal/layerfrom -> internal/layershared":      "shared-why",
			"internal/layerfrom -> internal/layerlow/gone":    "gone-why",
			"internal/elsewhere -> internal/layerlow":         "foreign-why",
			"internal/layerfrom internal/layerlow":            "malformed-why",
			"internal/layerquiet -> internal/layerlow/gone":   "quiet-why",
		},
	},
	{
		Name:   "t-other",
		From:   []string{"internal/elsewhere"},
		Forbid: []string{"internal/layerlow"},
		Allowed: map[string]string{
			"internal/layerfrom -> internal/layerlow": "other-why",
		},
	},
}

// layeringTestAnalyzer runs runLayering against the characterization table.
var layeringTestAnalyzer = &analysis.Analyzer{
	Name: "archlayering",
	Doc:  LayeringAnalyzer.Doc,
	Run:  func(pass *analysis.Pass) (any, error) { return runLayering(pass, layeringTable) },
}

// TestLayering_ViolationsAndAllowlistLiveness pins both halves: a forbidden
// import is reported at its spec unless an Except prefix or a live Allowed
// edge covers it, and each Allowed entry keyed to the package under analysis
// is reported at the package clause when its rule no longer covers the
// package, no longer forbids the dependency, or the import has gone.
func TestLayering_ViolationsAndAllowlistLiveness(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), layeringTestAnalyzer,
		ModulePath+"/internal/layerfrom", ModulePath+"/internal/layershared")
}

// TestLayering_LivenessOffReportsOnlyViolations pins the supplementary-pass
// switch: with allowlist liveness disabled, forbidden imports are still
// reported and no Allowed entry is judged.
func TestLayering_LivenessOffReportsOnlyViolations(t *testing.T) {
	t.Setenv(allowlistLivenessEnv, "0")
	analysistest.Run(t, analysistest.TestData(), layeringTestAnalyzer, ModulePath+"/internal/layerquiet")
}
