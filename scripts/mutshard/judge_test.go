package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// mut is one mutation record in a fake gomutants report.
type mut struct {
	file, status string
	line         int
	// typ, original and replacement are what gomutants records of the edit;
	// all optional.
	typ, original, replacement string
}

// gomutantsJSON builds a gomutants --output report from records, with the
// top-level counters gomutants itself would compute from them. file_name is
// deliberately NOT the module path: gomutants names a file relative to the
// run's common import prefix, and only the id carries the module path.
func gomutantsJSON(t *testing.T, muts ...mut) json.RawMessage {
	t.Helper()
	type file struct {
		Name      string              `json:"file_name"`
		Mutations []gomutantsMutation `json:"mutations"`
	}
	out := struct {
		gomutantsOutput
		Files []file `json:"files"`
	}{gomutantsOutput: gomutantsOutput{GoModule: "lab"}}
	byFile := map[string]int{}
	for i, m := range muts {
		k, ok := byFile[m.file]
		if !ok {
			k = len(out.Files)
			byFile[m.file] = k
			out.Files = append(out.Files, file{Name: "x.go"})
		}
		id := fmt.Sprintf("%s:F:CONDITIONALS_NEGATION#%d", m.file, i+1)
		out.Files[k].Mutations = append(out.Files[k].Mutations, gomutantsMutation{ID: id, Status: m.status, Line: m.line,
			Type: m.typ, Original: m.original, Replacement: m.replacement})
		switch m.status {
		case statusKilled:
			out.Killed++
		case statusLived:
			out.Lived++
		case statusNotCovered:
			out.NotCovered++
		case statusNotViable:
			out.NotViable++
		case statusTimedOut:
			out.TimedOut++
		case statusInfraError:
			out.InfraError++
		}
		out.Total++
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// many returns n records of one status in file.
func many(file, status string, n int) []mut {
	var ms []mut
	for i := 0; i < n; i++ {
		ms = append(ms, mut{file: file, status: status, line: i + 1})
	}
	return ms
}

var testStamp = stamp{scope: "diff:origin/main", head: "h", mergeBase: "m"}

func rep(shard, shards int, pkgs []string, g json.RawMessage) report {
	return report{Scope: testStamp.scope, Head: testStamp.head, MergeBase: testStamp.mergeBase,
		Shard: shard, Shards: shards, Packages: pkgs, Gomutants: g}
}

var twoShards = [][]string{{"a"}, {"b"}}

func concat(parts ...[]mut) []mut {
	var all []mut
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// THE VERDICT IS THE UNION'S. Each shard alone would be judged differently —
// shard 0 passes at 85%, shard 1 fails at 70% — but one run over both
// packages sees 24 killed of 30, which is 80%, and the gate fails AT the
// threshold, not only below it. A per-shard verdict (any rule that looks at a
// shard's own percentage) gets this wrong in one direction or the other.
func TestJudge_EfficacyIsJudgedOverTheUnionAtTheBoundary(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gomutantsJSON(t, concat(many("a/a.go", statusKilled, 17), many("a/a.go", statusLived, 3))...)),
		rep(1, 2, twoShards[1], gomutantsJSON(t, concat(many("b/b.go", statusKilled, 7), many("b/b.go", statusLived, 3))...)),
	}
	got, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80})
	if !errors.Is(err, errEfficacy) {
		t.Fatalf("err = %v, want errEfficacy (24/30 = 80%% is not above 80)", err)
	}
	if got.killed != 24 || got.lived != 6 {
		t.Errorf("tally = %+v, want 24 killed / 6 lived", got)
	}

	// One more kill in the weak shard lifts the UNION over the line, though
	// that shard alone is still under it.
	reports[1] = rep(1, 2, twoShards[1], gomutantsJSON(t, concat(many("b/b.go", statusKilled, 8), many("b/b.go", statusLived, 3))...))
	if _, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80}); err != nil {
		t.Errorf("25/31 = 80.6%%: err = %v, want pass", err)
	}
}

// A shard that legitimately tested nothing (its changed lines held no
// mutable token) is not refused on its own account, as long as it LOOKED:
// gomutants ran over its packages and wrote its report, zero mutants and all.
func TestJudge_AShardWithNoMutantsIsFineIfItLooked(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gomutantsJSON(t)),
		rep(1, 2, twoShards[1], gomutantsJSON(t, many("b/b.go", statusKilled, 9)...)),
	}
	if _, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80}); err != nil {
		t.Errorf("err = %v, want pass", err)
	}
}

// NOTHING SCORED IS NOTHING TO JUDGE (owner ruling #34, 2026-10-12). A union
// with no KILLED and no LIVED mutant has no efficacy to measure — its mutants
// all went uncovered, timed out or were not viable — so it passes, whatever
// the thresholds; the coverage gates still judge the uncovered lines. One that
// has a survivor and no kill is still 0% and refused.
func TestJudge_NothingKilledOrLivedAnywherePasses(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gomutantsJSON(t, mut{file: "a/a.go", status: statusNotCovered, line: 1}, mut{file: "a/a.go", status: statusTimedOut, line: 2})),
		rep(1, 2, twoShards[1], gomutantsJSON(t, mut{file: "b/b.go", status: statusNotViable, line: 1}, mut{file: "b/b.go", status: statusInfraError, line: 2})),
	}
	got, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80, mutantCoverage: 70})
	if err != nil {
		t.Fatalf("err = %v, want pass: nothing was killed or lived", err)
	}
	if got != (tally{notCovered: 1, timedOut: 1, notViable: 1, infraError: 1}) {
		t.Errorf("tally = %+v, want one each of not covered / timed out / not viable / infra error", got)
	}

	reports[1] = rep(1, 2, twoShards[1], gomutantsJSON(t, mut{file: "b/b.go", status: statusLived, line: 1}))
	if _, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80}); !errors.Is(err, errEfficacy) {
		t.Errorf("a survivor and no kill: err = %v, want errEfficacy", err)
	}
}

// Nothing scored passes ONLY over a union every shard vouched for. Without
// that, "nothing to mutate" is indistinguishable from a broken plan or a
// shard that never ran.
func TestJudge_NothingScoredIsRefusedWithoutEveryShardsEvidence(t *testing.T) {
	empty := gomutantsJSON(t)
	cases := []struct {
		name    string
		plan    [][]string
		reports []report
		want    error
	}{
		{"a shard's report is missing", twoShards, []report{rep(0, 2, twoShards[0], empty)}, errMissingShard},
		{"a shard never reported a gomutants run", twoShards, []report{rep(0, 2, twoShards[0], empty), rep(1, 2, twoShards[1], nil)}, errMeasuredNothing},
		{"a shard's report is not gomutants'", twoShards, []report{rep(0, 2, twoShards[0], empty), rep(1, 2, twoShards[1], json.RawMessage(`{}`))}, errUnreadable},
		{"no shard was given anything to look at", [][]string{nil, nil}, []report{rep(0, 2, nil, nil), rep(1, 2, nil, nil)}, errEfficacy},
		{"a dry run", twoShards, []report{rep(0, 2, twoShards[0], gomutantsJSON(t, mut{file: "a/a.go", status: statusPending, line: 1})), rep(1, 2, twoShards[1], empty)}, errMeasuredNothing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := judge(c.plan, testStamp, c.reports, thresholds{efficacy: 80}); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// The pass over a union with nothing scored says so, and prints the counts
// that were not scored, rather than a 0.00% efficacy beside a green verdict.
func TestReportVerdict_SaysNothingWasScoredAndCountsTheRest(t *testing.T) {
	var out bytes.Buffer
	reportVerdict(&out, 2, scope{base: "origin/main"}, tally{notCovered: 2, timedOut: 3, notViable: 4, infraError: 5}, nil)
	for _, want := range []string{msgNothingScored, "Not covered: 2", "Timed out: 3", "Not viable: 4", "Infra error: 5"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to carry %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "efficacy") {
		t.Errorf("output = %q, want no efficacy figure when nothing was scored", out.String())
	}
}

func TestJudge_MutantCoverageThresholdIsHonoured(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gomutantsJSON(t, concat(many("a/a.go", statusKilled, 5), many("a/a.go", statusNotCovered, 5))...)),
		rep(1, 2, twoShards[1], gomutantsJSON(t, many("b/b.go", statusKilled, 5)...)),
	}
	if _, err := judge(twoShards, testStamp, reports, thresholds{mutantCoverage: 70}); !errors.Is(err, errMutantCoverage) {
		t.Errorf("10/15 covered: err = %v, want errMutantCoverage", err)
	}
	if _, err := judge(twoShards, testStamp, reports, thresholds{mutantCoverage: 60}); err != nil {
		t.Errorf("10/15 covered vs 60: err = %v, want pass", err)
	}
}

// Every way a union can be incomplete or double-counted is refused before any
// arithmetic: a verdict over part of the work would be a verdict nobody asked
// for, and it would be lenient in exactly the way sharding must not be.
func TestJudge_RefusesAnIncompleteOrInconsistentUnion(t *testing.T) {
	good0 := gomutantsJSON(t, many("a/a.go", statusKilled, 9)...)
	good1 := gomutantsJSON(t, many("b/b.go", statusKilled, 9)...)
	cases := []struct {
		name    string
		plan    [][]string
		reports []report
		want    error
	}{
		{"no reports at all", twoShards, nil, errMissingShard},
		{"a shard's report is missing", twoShards, []report{rep(0, 2, twoShards[0], good0)}, errMissingShard},
		{"a shard reported twice", twoShards, []report{rep(0, 2, twoShards[0], good0), rep(0, 2, twoShards[0], good0)}, errMissingShard},
		{"shards disagree on the count", twoShards, []report{rep(0, 2, twoShards[0], good0), rep(1, 3, twoShards[1], good1)}, errStale},
		{"reports were cut for another shard count", twoShards, []report{rep(0, 1, []string{"a", "b"}, good0)}, errStale},
		{"a shard ran another plan", twoShards, []report{rep(0, 2, []string{"b"}, good0), rep(1, 2, twoShards[1], good1)}, errStale},
		{"a shard ran another commit", twoShards, []report{rep(0, 2, twoShards[0], good0), func() report {
			r := rep(1, 2, twoShards[1], good1)
			r.Head = "other"
			return r
		}()}, errStale},
		{"a shard tested another shard's package", twoShards, []report{rep(0, 2, twoShards[0], gomutantsJSON(t, concat(many("a/a.go", statusKilled, 9), many("b/b.go", statusLived, 1))...)), rep(1, 2, twoShards[1], good1)}, errLeak},
		{"a shard tested a subpackage it was not given", twoShards, []report{rep(0, 2, twoShards[0], gomutantsJSON(t, mut{file: "a/sub/s.go", status: statusKilled, line: 1})), rep(1, 2, twoShards[1], good1)}, errLeak},
		{"counters disagree with the records", twoShards, []report{rep(0, 2, twoShards[0], json.RawMessage(`{"go_module":"lab","files":[{"mutations":[{"id":"a/a.go:F:T#1","status":"KILLED"}]}],"mutants_total":1,"mutants_killed":7}`)), rep(1, 2, twoShards[1], good1)}, errUnreadable},
		{"the total disagrees with the records", twoShards, []report{rep(0, 2, twoShards[0], json.RawMessage(`{"go_module":"lab","files":[{"mutations":[{"id":"a/a.go:F:T#1","status":"KILLED"}]}],"mutants_total":2,"mutants_killed":1}`)), rep(1, 2, twoShards[1], good1)}, errUnreadable},
		{"a mutation with no id", twoShards, []report{rep(0, 2, twoShards[0], json.RawMessage(`{"go_module":"lab","files":[{"mutations":[{"status":"KILLED"}]}],"mutants_total":1,"mutants_killed":1}`)), rep(1, 2, twoShards[1], good1)}, errUnreadable},
		{"an unknown status", twoShards, []report{rep(0, 2, twoShards[0], gomutantsJSON(t, mut{file: "a/a.go", status: "MAYBE", line: 1})), rep(1, 2, twoShards[1], good1)}, errUnreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := judge(c.plan, testStamp, c.reports, thresholds{efficacy: 80}); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// The module's root package is ".", and a mutant in it names a bare file.
func TestJudge_TheRootPackageOwnsItsBareFiles(t *testing.T) {
	plan := [][]string{{"."}}
	reports := []report{rep(0, 1, plan[0], gomutantsJSON(t, many("root.go", statusKilled, 3)...))}
	if _, err := judge(plan, testStamp, reports, thresholds{efficacy: 80}); err != nil {
		t.Errorf("err = %v, want pass", err)
	}
}

// A shard the plan gave nothing to (more shards than candidate packages)
// carries no gomutants run, and needs none.
func TestJudge_AnEmptyShardNeedsNoRun(t *testing.T) {
	plan := [][]string{{"a"}, nil}
	reports := []report{rep(0, 2, plan[0], gomutantsJSON(t, many("a/a.go", statusKilled, 3)...)), rep(1, 2, nil, nil)}
	if _, err := judge(plan, testStamp, reports, thresholds{efficacy: 80}); err != nil {
		t.Errorf("err = %v, want pass", err)
	}
}

// The union sums EVERY count across shards — a count only one shard reports
// cannot show a sum that keeps only the last shard's.
func TestTally_AddSumsEveryCount(t *testing.T) {
	u := tally{1, 2, 3, 4, 5, 6}
	u.add(tally{10, 20, 30, 40, 50, 60})
	if u != (tally{11, 22, 33, 44, 55, 66}) {
		t.Errorf("sum = %+v", u)
	}
}

func TestExitCode_MirrorsGomutants(t *testing.T) {
	for err, want := range map[error]int{errEfficacy: 10, errMutantCoverage: 11, errLeak: 1, errMissingShard: 1, errRegressed: 1} {
		if got := exitCode(err); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", err, got, want)
		}
	}
}

// A refused verdict that names no mutant tells the author the tests are weak
// and not where: every survivor is listed, sorted by file then line whatever
// order the shards and the report held them in, with the edit when the report
// carries it.
func TestReportSurvivors_ListsEveryLivedAndNotCoveredMutantSorted(t *testing.T) {
	reports := []report{
		rep(0, 2, []string{"a"}, gomutantsJSON(t,
			mut{file: "a/z.go", status: statusLived, line: 9, typ: "ARITHMETIC_BASE", original: "a + b", replacement: "a - b"},
			mut{file: "a/z.go", status: statusKilled, line: 1})),
		rep(1, 2, []string{"b"}, gomutantsJSON(t,
			mut{file: "b/y.go", status: statusNotCovered, line: 4, typ: "CONDITIONALS_NEGATION"},
			mut{file: "a/a.go", status: statusLived, line: 30, typ: "INCREMENT_DECREMENT", original: "i++", replacement: "i--"})),
	}
	var out bytes.Buffer
	reportSurvivors(&out, reports, errEfficacy)
	want := []string{
		"a/a.go:30 LIVED INCREMENT_DECREMENT: i++ -> i--",
		"a/z.go:9 LIVED ARITHMETIC_BASE: a + b -> a - b",
		"b/y.go:4 NOT COVERED CONDITIONALS_NEGATION",
	}
	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(got) != len(want) {
		t.Fatalf("output = %q, want exactly %d lines", out.String(), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d = %q, want %q", i, got[i], w)
		}
	}
}

func TestReportSurvivors_PassingVerdictListsNothing(t *testing.T) {
	reports := []report{rep(0, 1, []string{"a"}, gomutantsJSON(t, mut{file: "a/a.go", status: statusLived, line: 1}))}
	var out bytes.Buffer
	reportSurvivors(&out, reports, nil)
	if out.Len() != 0 {
		t.Errorf("output = %q, want none on a pass", out.String())
	}
}

func TestReportSurvivors_AFailingVerdictWithNoSurvivorListsNothing(t *testing.T) {
	reports := []report{rep(0, 1, []string{"a"}, gomutantsJSON(t, mut{file: "a/a.go", status: statusKilled, line: 1}))}
	var out bytes.Buffer
	reportSurvivors(&out, reports, errEfficacy)
	if out.Len() != 0 {
		t.Errorf("output = %q, want none without a survivor", out.String())
	}
}

// A long list is capped so a whole-tree failure does not bury its verdict; the
// cap's remainder is counted, not dropped silently.
func TestReportSurvivors_CapsALongListAndCountsTheRest(t *testing.T) {
	reports := []report{rep(0, 1, []string{"a"}, gomutantsJSON(t, many("a/a.go", statusLived, maxSurvivorsListed+7)...))}
	var out bytes.Buffer
	reportSurvivors(&out, reports, errEfficacy)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != maxSurvivorsListed+1 {
		t.Fatalf("got %d lines, want %d mutants + the remainder line", len(lines), maxSurvivorsListed)
	}
	if lines[maxSurvivorsListed] != "+7 more" {
		t.Errorf("last line = %q, want %q", lines[maxSurvivorsListed], "+7 more")
	}
}

// Through the command line: the verdict's exit code is what it was, and the
// list rides on the same stdout as the counts.
func TestCommands_AggregateListsSurvivorsOnAFailingVerdict(t *testing.T) {
	_, launcher, _ := cmdModule(t)
	reports := t.TempDir()
	t.Setenv("STATUS", "LIVED")
	runShards(t, launcher, reports, "0", "1")
	code, out := mutshard("aggregate", "-scope", "tree", "-reports", reports, "-efficacy", "80")
	if code != 10 {
		t.Fatalf("exit = %d, want 10\n%s", code, out)
	}
	if !strings.Contains(out, "f.go:1 LIVED") {
		t.Errorf("output = %q, want the lived mutant listed", out)
	}
}

// One survivor is one line: a block mutant's snippet spans lines and is
// collapsed, and a very long one is cut.
func TestReportSurvivors_AnEditIsOneBoundedLine(t *testing.T) {
	long := strings.Repeat("x", maxSnippet+10)
	reports := []report{rep(0, 1, []string{"a"}, gomutantsJSON(t,
		mut{file: "a/a.go", status: statusLived, line: 1, typ: "BRANCH_IF", original: "{\n\t\tcontinue\n\t}", replacement: long},
		mut{file: "a/a.go", status: statusLived, line: 2, typ: "T", original: "only"},
		mut{file: "a/a.go", status: statusLived, line: 3, typ: "T", replacement: "only"}))}
	var out bytes.Buffer
	reportSurvivors(&out, reports, errEfficacy)
	want := "a/a.go:1 LIVED BRANCH_IF: { continue } -> " + strings.Repeat("x", maxSnippet) + "...\n" +
		"a/a.go:2 LIVED T: only ->\n" +
		"a/a.go:3 LIVED T:  -> only\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// The ties of the sort are broken the same way every run: line within a file,
// then id within a line.
func TestReportSurvivors_SortsByLineThenIDWithinAFile(t *testing.T) {
	reports := []report{rep(0, 1, []string{"a"}, gomutantsJSON(t,
		mut{file: "a/a.go", status: statusLived, line: 7, typ: "B"},
		mut{file: "a/a.go", status: statusLived, line: 2, typ: "Z"},
		mut{file: "a/a.go", status: statusLived, line: 7, typ: "A"}))}
	var out bytes.Buffer
	reportSurvivors(&out, reports, errEfficacy)
	// gomutantsJSON numbers ids in input order, so line 7's two records tie on
	// everything but id, and the one listed first by the report sorts first.
	want := "a/a.go:2 LIVED Z\na/a.go:7 LIVED B\na/a.go:7 LIVED A\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A shard the plan gave nothing carries no report, and a refusal to judge has
// no verdict about survivors: neither prints a list or breaks the other
// shards' list.
func TestReportSurvivors_SkipsEmptyShardsAndRefusals(t *testing.T) {
	reports := []report{
		rep(0, 2, nil, nil),
		rep(1, 2, []string{"b"}, gomutantsJSON(t, mut{file: "b/b.go", status: statusLived, line: 1})),
	}
	var out bytes.Buffer
	reportSurvivors(&out, reports, errEfficacy)
	if out.String() != "b/b.go:1 LIVED\n" {
		t.Errorf("output = %q", out.String())
	}
	out.Reset()
	reportSurvivors(&out, reports, errStale)
	if out.Len() != 0 {
		t.Errorf("a refusal listed %q", out.String())
	}
}

// The cap is on the count listed: exactly the cap lists everything with no
// remainder line, one over lists the cap and counts one.
func TestReportSurvivors_TheCapBoundary(t *testing.T) {
	for _, c := range []struct {
		n    int
		last string
	}{{maxSurvivorsListed, "a/a.go:50 LIVED"}, {maxSurvivorsListed + 1, "+1 more"}} {
		reports := []report{rep(0, 1, []string{"a"}, gomutantsJSON(t, many("a/a.go", statusLived, c.n)...))}
		var out bytes.Buffer
		reportSurvivors(&out, reports, errEfficacy)
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if got := lines[len(lines)-1]; got != c.last {
			t.Errorf("%d survivors: last line = %q, want %q", c.n, got, c.last)
		}
	}
}
