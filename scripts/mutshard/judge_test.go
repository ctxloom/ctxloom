package main

import (
	"encoding/json"
	"errors"
	"testing"
)

// mut is one mutation record in a fake gremlins report.
type mut struct {
	file, status string
	line         int
}

// gremlinsJSON builds a gremlins --output report from records, with the
// top-level counters gremlins itself would compute from them.
func gremlinsJSON(t *testing.T, muts ...mut) json.RawMessage {
	t.Helper()
	out := gremlinsOutput{}
	byFile := map[string]int{}
	for _, m := range muts {
		i, ok := byFile[m.file]
		if !ok {
			i = len(out.Files)
			byFile[m.file] = i
			out.Files = append(out.Files, gremlinsFile{Filename: m.file})
		}
		out.Files[i].Mutations = append(out.Files[i].Mutations, gremlinsMutation{Type: "CONDITIONALS_NEGATION", Status: m.status, Line: m.line, Column: 1})
		switch m.status {
		case statusKilled:
			out.Killed++
		case statusLived:
			out.Lived++
		case statusNotCovered:
			out.NotCovered++
		case statusNotViable:
			out.NotViable++
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

func rep(shard, shards int, files []string, g json.RawMessage) report {
	return report{Scope: testStamp.scope, Head: testStamp.head, MergeBase: testStamp.mergeBase,
		Shard: shard, Shards: shards, Files: files, Gremlins: g}
}

var twoShards = [][]string{{"a.go"}, {"b.go"}}

func concat(parts ...[]mut) []mut {
	var all []mut
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// THE VERDICT IS THE UNION'S. Each shard alone would be judged differently —
// shard 0 passes at 85%, shard 1 fails at 70% — but the gate that existed
// before sharding saw 24 killed of 30, which is 80%, and gremlins fails
// efficacy AT the threshold, not only below it. A per-shard verdict (any rule
// that looks at a shard's own percentage) gets this wrong in one direction or
// the other.
func TestJudge_EfficacyIsJudgedOverTheUnionAtGremlinsBoundary(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gremlinsJSON(t, concat(many("a.go", statusKilled, 17), many("a.go", statusLived, 3))...)),
		rep(1, 2, twoShards[1], gremlinsJSON(t, concat(many("b.go", statusKilled, 7), many("b.go", statusLived, 3))...)),
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
	reports[1] = rep(1, 2, twoShards[1], gremlinsJSON(t, concat(many("b.go", statusKilled, 8), many("b.go", statusLived, 3))...))
	if _, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80}); err != nil {
		t.Errorf("25/31 = 80.6%%: err = %v, want pass", err)
	}
}

// A shard that legitimately tested nothing (its diffed lines held no
// mutable token) is not refused on its own account — the unsharded gate never
// refused a file for that — as long as it demonstrably LOOKED: gremlins
// walked the tree and reported.
func TestJudge_AShardWithNoTestedMutantsIsFineIfItLooked(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gremlinsJSON(t, mut{"other.go", statusSkipped, 1})),
		rep(1, 2, twoShards[1], gremlinsJSON(t, many("b.go", statusKilled, 9)...)),
	}
	if _, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80}); err != nil {
		t.Errorf("err = %v, want pass", err)
	}
}

// Nothing killed across the whole union is 0% — refused exactly as gremlins
// refuses an empty changeset.
func TestJudge_NothingKilledAnywhereIsRefused(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gremlinsJSON(t, mut{"a.go", statusNotCovered, 1})),
		rep(1, 2, twoShards[1], gremlinsJSON(t, mut{"b.go", statusSkipped, 1})),
	}
	if _, err := judge(twoShards, testStamp, reports, thresholds{efficacy: 80}); !errors.Is(err, errEfficacy) {
		t.Errorf("err = %v, want errEfficacy", err)
	}
}

func TestJudge_MutantCoverageThresholdIsHonoured(t *testing.T) {
	reports := []report{
		rep(0, 2, twoShards[0], gremlinsJSON(t, concat(many("a.go", statusKilled, 5), many("a.go", statusNotCovered, 5))...)),
		rep(1, 2, twoShards[1], gremlinsJSON(t, many("b.go", statusKilled, 5)...)),
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
	good0 := gremlinsJSON(t, many("a.go", statusKilled, 9)...)
	good1 := gremlinsJSON(t, many("b.go", statusKilled, 9)...)
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
		{"reports were cut for another shard count", twoShards, []report{rep(0, 1, []string{"a.go", "b.go"}, good0)}, errStale},
		{"a shard ran another plan", twoShards, []report{rep(0, 2, []string{"b.go"}, good0), rep(1, 2, twoShards[1], good1)}, errStale},
		{"a shard ran another commit", twoShards, []report{rep(0, 2, twoShards[0], good0), func() report {
			r := rep(1, 2, twoShards[1], good1)
			r.Head = "other"
			return r
		}()}, errStale},
		{"a shard never reported a gremlins run", twoShards, []report{rep(0, 2, twoShards[0], nil), rep(1, 2, twoShards[1], good1)}, errMeasuredNothing},
		{"gremlins walked nothing", twoShards, []report{rep(0, 2, twoShards[0], gremlinsJSON(t)), rep(1, 2, twoShards[1], good1)}, errMeasuredNothing},
		{"a shard tested another shard's file", twoShards, []report{rep(0, 2, twoShards[0], gremlinsJSON(t, concat(many("a.go", statusKilled, 9), many("b.go", statusLived, 1))...)), rep(1, 2, twoShards[1], good1)}, errLeak},
		{"counters disagree with the records", twoShards, []report{rep(0, 2, twoShards[0], json.RawMessage(`{"files":[{"file_name":"a.go","mutations":[{"status":"KILLED"}]}],"mutants_killed":7}`)), rep(1, 2, twoShards[1], good1)}, errUnreadable},
		{"an unknown status", twoShards, []report{rep(0, 2, twoShards[0], gremlinsJSON(t, mut{"a.go", "MAYBE", 1})), rep(1, 2, twoShards[1], good1)}, errUnreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := judge(c.plan, testStamp, c.reports, thresholds{efficacy: 80}); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// A shard the plan gave nothing to (more shards than candidate files) carries
// no gremlins run, and needs none.
func TestJudge_AnEmptyShardNeedsNoRun(t *testing.T) {
	plan := [][]string{{"a.go"}, nil}
	reports := []report{rep(0, 2, plan[0], gremlinsJSON(t, many("a.go", statusKilled, 3)...)), rep(1, 2, nil, nil)}
	if _, err := judge(plan, testStamp, reports, thresholds{efficacy: 80}); err != nil {
		t.Errorf("err = %v, want pass", err)
	}
}

func TestExitCode_MirrorsGremlins(t *testing.T) {
	for err, want := range map[error]int{errEfficacy: 10, errMutantCoverage: 11, errLeak: 1, errMissingShard: 1} {
		if got := exitCode(err); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", err, got, want)
		}
	}
}
