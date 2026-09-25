package archlint_test

import (
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/ctxloom/ctxloom/internal/shared/archlint"
)

// fixture is the import path of a package under testdata/src. The fixtures
// live at their real module-relative paths because every rule is scoped by
// the directory it is written in terms of.
//
// A fixture with _test.go files keeps its planted violations OUT of its
// production files: the driver hands a rule the test variant too, which
// repeats every production file, and analysistest expects each want in every
// pass that contains it — while the rule, correctly, reports it once.
func fixture(rel string) string { return archlint.ModulePath + "/" + rel }

// run applies one analyzer to the named fixtures.
func run(t *testing.T, a *analysis.Analyzer, rels ...string) {
	t.Helper()
	pkgs := make([]string, len(rels))
	for i, rel := range rels {
		pkgs[i] = fixture(rel)
	}
	analysistest.Run(t, analysistest.TestData(), a, pkgs...)
}

// TestDocComment_CoversProductionAndTestFiles plants a restated doc in a
// production file, an in-package test file and an external test package.
func TestDocComment_CoversProductionAndTestFiles(t *testing.T) {
	run(t, archlint.DocCommentAnalyzer, "internal/docprod", "internal/docplant")
}

// TestLockDiscipline_WriteServersAndRemoveServersAreWrites plants unlocked
// read-modify-writes through WriteServers and RemoveServers, and shows the
// exempt primitive file is not judged.
func TestLockDiscipline_WriteServersAndRemoveServersAreWrites(t *testing.T) {
	run(t, archlint.LockDisciplineAnalyzer, "internal/engines/claude/lockplant", "internal/core/agent")
}

// TestLedgerDiscipline_ManagedWriteNeedsARecord plants a managed write through
// WriteServers with no ownership record beside one that keeps a ledger.
func TestLedgerDiscipline_ManagedWriteNeedsARecord(t *testing.T) {
	run(t, archlint.LedgerDisciplineAnalyzer, "internal/core/agent/ledgerplant", "internal/core/agent")
}
