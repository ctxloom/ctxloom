package archlint

import (
	"strings"

	"golang.org/x/tools/go/analysis"
)

// TestSupportPrefix is the package tree that must not be reachable from
// shipped code. Matching is by subtree, so internal/testsupport and
// internal/testsupport/parity are both covered while a hypothetical
// internal/testsupportish is not.
const TestSupportPrefix = ModulePath + "/internal/testsupport"

// testSupportImporters are the non-shipped test-harness packages permitted to
// import the test-only tree from a production file. Each entry is a
// module-relative directory mapped to why it is admitted.
//
// An entry is a deliberate, reviewable admission. The analyzer reports an
// entry that has stopped being true, so a stale exemption cannot silently
// cover whatever later takes that directory's name.
var testSupportImporters = map[string]string{
	"tests/integration/testenv": "shared harness for the -tags integration suite; never linked into a binary",
	"tests/acceptance":          "godog acceptance suite, compiled only under -tags acceptance; never linked into a binary",
}

// reachesTestSupport is exported by a package that can reach the test-only
// tree, directly or through an import. Facts travel along import edges, which
// is exactly the shape of a reachability closure: a main package learns that
// something six edges down pulled the tree in.
type reachesTestSupport struct {
	// Via names the direct import that carried the dependency, so a violation
	// reports the next hop rather than only the endpoint.
	Via string
}

func (*reachesTestSupport) AFact() {}

func (f *reachesTestSupport) String() string { return "reaches test-only tree via " + f.Via }

// TestSupportAnalyzer enforces that test-only machinery stays out of shipped
// code. internal/testsupport is an ordinary Go package: nothing in the
// language or the build stops a production file importing it, and the only
// symptom would be a slightly larger binary carrying a reflection sweep and a
// dependency on testing.
//
// Two rules, deliberately overlapping. The whole-module rule fails at the
// package that introduces the import, so the violation is named where it was
// written. The reachability rule fails at the binary, which is the
// consequence that actually matters and admits no exemptions.
var TestSupportAnalyzer = &analysis.Analyzer{
	Name: "archtestsupport",
	Doc:  "test-only packages (internal/testsupport) must not be reachable from shipped code",
	Run:  runTestSupport,
	FactTypes: []analysis.Fact{
		(*reachesTestSupport)(nil),
	},
}

// isTestSupport reports whether an import path is inside the test-only tree.
func isTestSupport(importPath string) bool {
	return importPath == TestSupportPrefix || strings.HasPrefix(importPath, TestSupportPrefix+"/")
}

func runTestSupport(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || UnderSubtree(dir, "internal/testsupport") {
		// The tree may of course import itself.
		return nil, nil
	}

	imports := ImportPaths(pass)
	_, admitted := testSupportImporters[dir]

	// Direct imports. An admitted harness is exempt from the report but still
	// propagates the fact, so a binary that links one is still caught.
	direct := ""
	for ip, spec := range imports {
		if !isTestSupport(ip) {
			continue
		}
		direct = ip
		if admitted {
			continue
		}
		pass.Reportf(spec.Pos(),
			"package %s imports %s from a production file — test-only machinery must not be reachable "+
				"from ordinary code. Move the helper into a _test.go file, or, if this package is itself a "+
				"test harness that is never linked into a binary, add it to testSupportImporters in "+
				"internal/archlint/testsupport.go with a reason.", dir, ip)
	}

	// The allowlist must stay live: an entry naming a package that no longer
	// imports the tree would silently exempt whatever import lands there next.
	if admitted && direct == "" && allowlistLivenessEnabled() {
		pass.Reportf(pass.Files[0].Package,
			"testSupportImporters exempts %q but it no longer imports %s — delete the entry, or it will "+
				"silently exempt whatever import lands there next", dir, TestSupportPrefix)
	}

	// Propagate reachability, and fail at any shipped binary that has it.
	via := direct
	if via == "" {
		for ip := range imports {
			imported := importedPkg(pass, ip)
			if imported == nil {
				continue
			}
			var fact reachesTestSupport
			if pass.ImportPackageFact(imported, &fact) {
				via = ip
				break
			}
		}
	}
	if via == "" {
		return nil, nil
	}
	pass.ExportPackageFact(&reachesTestSupport{Via: via})

	if pass.Pkg.Name() == "main" && UnderSubtree(dir, "cmd") {
		pass.Reportf(pass.Files[0].Package,
			"shipped binary %s reaches the test-only tree through %s — nothing test-only may be linked "+
				"into a released binary, and this rule admits no exemptions", dir, via)
	}
	return nil, nil
}
