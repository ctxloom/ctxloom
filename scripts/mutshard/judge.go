package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"
)

// report is one shard's result: what it was asked to cover, on which commit,
// and gomutants' own --output for the run (absent when the plan gave it
// nothing).
type report struct {
	Scope     string          `json:"scope"`
	Head      string          `json:"head"`
	MergeBase string          `json:"merge_base,omitempty"`
	Shard     int             `json:"shard"`
	Shards    int             `json:"shards"`
	Packages  []string        `json:"packages"`
	Gomutants json.RawMessage `json:"gomutants,omitempty"`
}

// stamp is what every report of one run must agree on with the aggregate.
type stamp struct {
	scope, head, mergeBase string
}

// gomutantsOutput mirrors gomutants' --output format (internal/report,
// JSONReport) — only the parts judged here.
type gomutantsOutput struct {
	GoModule   string          `json:"go_module"`
	Files      []gomutantsFile `json:"files"`
	Total      int             `json:"mutants_total"`
	Killed     int             `json:"mutants_killed"`
	Lived      int             `json:"mutants_lived"`
	NotViable  int             `json:"mutants_not_viable"`
	NotCovered int             `json:"mutants_not_covered"`
	TimedOut   int             `json:"mutants_timed_out"`
	InfraError int             `json:"mutants_infra_error"`
}

type gomutantsFile struct {
	Mutations []gomutantsMutation `json:"mutations"`
}

// gomutantsMutation is one mutant. Its file is read from ID, never from the
// report's file_name: gomutants names a file relative to the longest import
// path the run's packages share, so file_name changes with the package set.
// The stable ID leads with the module-relative path ("dir/f.go:Func:TYPE#n").
type gomutantsMutation struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Line   int    `json:"line"`
}

// file is the module-relative file the mutant is in.
func (m gomutantsMutation) file() string {
	f, _, _ := strings.Cut(m.ID, ":")
	return f
}

// gomutants' mutator.MutantStatus strings.
const (
	statusKilled     = "KILLED"
	statusLived      = "LIVED"
	statusNotCovered = "NOT COVERED"
	statusTimedOut   = "TIMED OUT"
	statusNotViable  = "NOT VIABLE"
	statusInfraError = "INFRA ERROR"
	statusPending    = "PENDING"
)

type thresholds struct {
	efficacy, mutantCoverage float64
}

type tally struct {
	killed, lived, notCovered, timedOut, notViable, infraError int
}

func (t *tally) add(o tally) {
	t.killed += o.killed
	t.lived += o.lived
	t.notCovered += o.notCovered
	t.timedOut += o.timedOut
	t.notViable += o.notViable
	t.infraError += o.infraError
}

// scored counts the mutants an efficacy can be measured over: KILLED and LIVED.
func (t tally) scored() int {
	return t.killed + t.lived
}

// total counts every mutant the run reported.
func (t tally) total() int {
	return t.killed + t.lived + t.notCovered + t.timedOut + t.notViable + t.infraError
}

// survived counts the mutants nothing verified: LIVED (a test ran and did not
// notice) and NOT COVERED (no test ran the line).
func (t tally) survived() int {
	return t.lived + t.notCovered
}

// efficacy and mutantCoverage are gomutants' gate formulas
// (--threshold-efficacy, --threshold-mcover), including 0 when nothing was
// killed.
func (t tally) efficacy() float64 {
	if t.killed == 0 {
		return 0
	}
	return float64(t.killed) / float64(t.killed+t.lived) * 100
}

func (t tally) mutantCoverage() float64 {
	if t.killed+t.lived == 0 {
		return 0
	}
	return float64(t.killed+t.lived) / float64(t.killed+t.lived+t.notCovered) * 100
}

var (
	errMissingShard    = errors.New("the union is incomplete: a shard's report is missing or duplicated")
	errStale           = errors.New("a shard's report is for a different run")
	errMeasuredNothing = errors.New("a shard measured nothing")
	errLeak            = errors.New("a shard mutated a package the plan gave another shard")
	errUnreadable      = errors.New("a shard's gomutants report cannot be read")
	errEfficacy        = errors.New("test efficacy is not above the threshold")
	errMutantCoverage  = errors.New("mutant coverage is not above the threshold")
)

// exitCode is gomutants' own for its two threshold failures, 1 for a refusal.
func exitCode(err error) int {
	switch {
	case errors.Is(err, errEfficacy):
		return 10
	case errors.Is(err, errMutantCoverage):
		return 11
	}
	return 1
}

// judge applies the gate to the UNION of the shards' results, exactly as one
// unsharded run would apply it to its own: the counts are summed and the
// thresholds are judged once — failing AT the threshold, not only below it.
// Before any arithmetic it refuses a union it cannot vouch for: a missing,
// duplicated or stale shard, a shard given packages that carries no gomutants
// report, and a shard that tested a package it was not given.
//
// A vouched-for union with NOTHING SCORED — no KILLED and no LIVED mutant —
// passes with nothing to judge: its mutants, if any, all went uncovered, timed
// out or were not viable, so there is no efficacy to measure, and the coverage
// gates still judge the uncovered lines (owner ruling #34, 2026-10-12). It
// needs at least one shard that was given packages, since only those had to
// show they looked; without one, zero is not evidence of anything and is
// judged.
func judge(shards [][]string, st stamp, reports []report, th thresholds) (tally, error) {
	var total tally
	byShard, err := indexReports(len(shards), reports)
	if err != nil {
		return total, err
	}
	looked := false
	for k, pkgs := range shards {
		r, ok := byShard[k]
		if !ok {
			return total, fmt.Errorf("%w: no report from shard %d of %d", errMissingShard, k, len(shards))
		}
		t, err := vetShard(k, pkgs, r, st)
		if err != nil {
			return total, err
		}
		total.add(t)
		looked = looked || len(pkgs) > 0
	}
	if looked && total.scored() == 0 {
		return total, nil
	}
	return total, th.assess(total)
}

// indexReports keys the reports by shard, refusing one cut for another shard
// count, a duplicate, and an index outside the plan.
func indexReports(n int, reports []report) (map[int]report, error) {
	byShard := map[int]report{}
	for _, r := range reports {
		if r.Shards != n {
			return nil, fmt.Errorf("%w: shard %d was cut for %d shards, the plan has %d", errStale, r.Shard, r.Shards, n)
		}
		if _, dup := byShard[r.Shard]; dup || r.Shard < 0 || r.Shard >= n {
			return nil, fmt.Errorf("%w: shard %d reported more than once or out of range", errMissingShard, r.Shard)
		}
		byShard[r.Shard] = r
	}
	return byShard, nil
}

// vetShard holds shard k's report to the plan and the stamp, then tallies it.
// A shard the plan gave nothing counts nothing.
func vetShard(k int, pkgs []string, r report, st stamp) (tally, error) {
	if r.Scope != st.scope || r.Head != st.head || r.MergeBase != st.mergeBase {
		return tally{}, fmt.Errorf("%w: shard %d ran %s at %s (merge base %q), the aggregate is judging %s at %s (merge base %q)",
			errStale, k, r.Scope, r.Head, r.MergeBase, st.scope, st.head, st.mergeBase)
	}
	if !reflect.DeepEqual(nonNil(r.Packages), nonNil(pkgs)) {
		return tally{}, fmt.Errorf("%w: shard %d covered %v, the plan gives it %v", errStale, k, r.Packages, pkgs)
	}
	if len(pkgs) == 0 {
		return tally{}, nil
	}
	return shardTally(k, pkgs, r.Gomutants)
}

// assess is gomutants' own judgement of the union: failing AT a threshold, not
// only below it; a zero threshold is not judged.
func (th thresholds) assess(t tally) error {
	if th.efficacy > 0 && t.efficacy() <= th.efficacy {
		return fmt.Errorf("%w: %.2f%% <= %.2f%%", errEfficacy, t.efficacy(), th.efficacy)
	}
	if th.mutantCoverage > 0 && t.mutantCoverage() <= th.mutantCoverage {
		return fmt.Errorf("%w: %.2f%% <= %.2f%%", errMutantCoverage, t.mutantCoverage(), th.mutantCoverage)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// shardTally reads one shard's gomutants report into counts. Evidence that
// the shard LOOKED is the report itself: gomutants writes it only after
// resolving the packages, collecting coverage and discovering mutants, and a
// run that failed exits nonzero, which fails the shard before it reports. A
// diff whose changed lines hold no mutable token reports zero mutants, and
// that is a look, not a failure.
func shardTally(k int, pkgs []string, raw json.RawMessage) (tally, error) {
	var t tally
	if len(raw) == 0 {
		return t, fmt.Errorf("%w: shard %d was given %d package(s) and carries no gomutants report", errMeasuredNothing, k, len(pkgs))
	}
	var out gomutantsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return t, fmt.Errorf("%w: shard %d: %v", errUnreadable, k, err)
	}
	if out.GoModule == "" {
		return t, fmt.Errorf("%w: shard %d: the report names no go_module — it is not a gomutants report", errUnreadable, k)
	}
	mine := map[string]bool{}
	for _, p := range pkgs {
		mine[p] = true
	}
	for _, f := range out.Files {
		for _, m := range f.Mutations {
			if err := t.count(k, m); err != nil {
				return t, err
			}
			if !mine[path.Dir(m.file())] {
				return t, fmt.Errorf("%w: shard %d scored %s %s:%d", errLeak, k, m.Status, m.file(), m.Line)
			}
		}
	}
	return t, t.agrees(k, out)
}

// count adds one mutation record to t.
func (t *tally) count(k int, m gomutantsMutation) error {
	if m.file() == "" {
		return fmt.Errorf("%w: shard %d: a mutation carries no id", errUnreadable, k)
	}
	switch m.Status {
	case statusKilled:
		t.killed++
	case statusLived:
		t.lived++
	case statusNotCovered:
		t.notCovered++
	case statusTimedOut:
		t.timedOut++
	case statusNotViable:
		t.notViable++
	case statusInfraError:
		t.infraError++
	case statusPending:
		// A dry run's status; a real run never leaves one.
		return fmt.Errorf("%w: shard %d: %s:%d is PENDING — the run was never executed", errMeasuredNothing, k, m.file(), m.Line)
	default:
		return fmt.Errorf("%w: shard %d: unknown status %q at %s:%d", errUnreadable, k, m.Status, m.file(), m.Line)
	}
	return nil
}

// agrees refuses a report whose mutation records and summary counters differ.
func (t tally) agrees(k int, out gomutantsOutput) error {
	rec := []int{t.total(), t.killed, t.lived, t.notCovered, t.notViable, t.timedOut, t.infraError}
	sum := []int{out.Total, out.Killed, out.Lived, out.NotCovered, out.NotViable, out.TimedOut, out.InfraError}
	if !reflect.DeepEqual(rec, sum) {
		return fmt.Errorf("%w: shard %d: records say %v total/killed/lived/not-covered/not-viable/timed-out/infra-error, gomutants' counters say %v",
			errUnreadable, k, rec, sum)
	}
	return nil
}
