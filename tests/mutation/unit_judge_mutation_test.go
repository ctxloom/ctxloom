//go:build mutation

package mutation

import "testing"

// unitMutationTargets pair a source file with the SINGLE test that claims to
// verify it. Adding an entry is a data change.
//
// This table is deliberately separate from mutationTargets even though both
// hold mutationTarget: the two are different cost classes with different
// lifecycles. The acceptance table is a ratcheted, scheduled measurement of
// what the suite really covers. This one is an authoring-time check — you run
// the entry for the test you just wrote, read the verdict, and move on. It is
// not a CI gate and carries no baseline, because "did it get worse since last
// time" is not a question an authoring-time check has.
var unitMutationTargets = []mutationTarget{
	{
		Name:          "premise_instruction",
		SourceRelPath: "internal/adapters/operations/premise.go",
		Judge: unitJudge{
			Pkg: "./internal/adapters/operations",
			Run: "^TestRenderPremiseIndex_KeepsTheThreeMeasuredProperties$",
		},
	},
}

// TestUnitMutation releases ooze against each unit target, judging every mutant
// with that entry's single test.
//
// WHY THIS EXISTS RATHER THAN HAND MUTATION. The rule is that a test is not
// passing until a mutation against it dies, and the obvious way to satisfy it —
// edit the source, run the test, revert — writes the REAL source file and
// depends on the revert happening. An interruption between the two leaves a
// mutated file in the working tree. ooze mutates inside a laboratory: a tmpdir
// where every file is a symlink back to the checkout except the mutated one, so
// the source tree is never written at all.
//
// It is also cheaper. Measured on premise_instruction: 22 mutants in 130s
// (~6s each) against ~10s for a single hand mutation — and it mutates the whole
// file rather than the one line someone thought to try.
//
// EXPECT SURVIVORS, and do not read the score as a quality verdict. The judge is
// one test; every mutant elsewhere in the file survives by construction. The
// signal is whether mutants in the code the test NAMES die — a run where
// nothing dies means the test is vacuous.
func TestUnitMutation(t *testing.T) {
	for _, target := range unitMutationTargets {
		t.Run(target.Name, func(t *testing.T) {
			target.release(t)
		})
	}
}
